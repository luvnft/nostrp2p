package api_server

import (
	"fmt"
	"github.com/ant0ine/go-json-rest/rest"
	"github.com/ryogrid/nostrp2p/core"
	"github.com/ryogrid/nostrp2p/glo_val"
	"github.com/ryogrid/nostrp2p/np2p_util"
	"github.com/ryogrid/nostrp2p/schema"
	"log"
	"math"
	"net/http"
	"slices"
)

type NoArgReq struct {
}

type EventsResp struct {
	Events []schema.Np2pEventForREST `json:"results"`
}
type GeneralResp struct {
	Status string
}

type ApiServer struct {
	buzzPeer *core.Np2pPeer
}

func NewApiServer(peer *core.Np2pPeer) *ApiServer {
	return &ApiServer{peer}
}

func (s *ApiServer) publishHandler(w rest.ResponseWriter, req *rest.Request) {
	input := schema.Np2pEventForREST{}
	err := req.DecodeJsonPayload(&input)

	if err != nil {
		fmt.Println(err)
		rest.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if glo_val.DenyWriteMode {
		rest.Error(w, "Write is denied", http.StatusNotAcceptable)
		return
	}

	if input.Verify() == false {
		rest.Error(w, "Invalid Sig", http.StatusBadRequest)
		return
	}

	// TODO: need to check Sig (ApiServer::publishHandler)

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	switch input.Kind {
	case core.KIND_EVT_POST: // including quote repost
		s.sendPost(w, &input)
	case core.KIND_EVT_REPOST:
		s.sendRePost(w, &input)
	case core.KIND_EVT_PROFILE:
		s.updateProfile(w, &input)
	case core.KIND_EVT_FOLLOW_LIST:
		s.setOrUpdateFollowList(w, &input)
	case core.KIND_EVT_REACTION:
		s.sendReaction(w, &input)
	default:
		rest.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}

func (s *ApiServer) sendRePost(w rest.ResponseWriter, input *schema.Np2pEventForREST) {
	evt := schema.NewNp2pEventFromREST(input)
	s.buzzPeer.MessageMan.BcastOwnPost(evt)

	// store for myself
	s.buzzPeer.MessageMan.DataMan.StoreEvent(evt)

	w.WriteJson(&EventsResp{})
}

func (s *ApiServer) sendPost(w rest.ResponseWriter, input *schema.Np2pEventForREST) {
	if input.Content == "" {
		rest.Error(w, "Content is required", 400)
		return
	}

	// if mention, reply or quote repost, extract related user's pubkey
	sendDests := make([]string, 0)
	isQuoteRpost := false
	if input.Tags != nil {
		for _, tag := range input.Tags {
			if tag[0] == "p" && tag[1] != glo_val.SelfPubkeyStr {
				// extract short pubkey from p tags hex string value
				sendDests = append(sendDests, tag[1])
			}
			if tag[0] == "q" {
				isQuoteRpost = true
			}
		}
	}

	evt := schema.NewNp2pEventFromREST(input)
	if len(sendDests) > 0 && !isQuoteRpost {
		// send to specified users because post is mention or reply
		resendDests := make([]uint64, 0)
		for _, dest := range sendDests {
			err := s.buzzPeer.MessageMan.UnicastEventData(dest, evt)
			if err != nil {
				resendDests = append(resendDests, np2p_util.Get6ByteUint64FromHexPubKeyStr(dest))
				fmt.Println(err)
			}
		}
		// destination server is offline
		// so add event to retry queue
		s.buzzPeer.MessageMan.DataMan.AddReSendNeededEvent(resendDests, evt, true)
	} else {
		// normal post or quote repost
		s.buzzPeer.MessageMan.BcastOwnPost(evt)
	}

	// store for myself
	// if destination server is offline, this event will be sent again (when unicast)
	s.buzzPeer.MessageMan.DataMan.StoreEvent(evt)

	w.WriteJson(&EventsResp{})
}

func (s *ApiServer) updateProfile(w rest.ResponseWriter, input *schema.Np2pEventForREST) {
	if input.Tags == nil {
		rest.Error(w, "Tags is null", http.StatusBadRequest)
		return
	}

	evt := schema.NewNp2pEventFromREST(input)
	if *glo_val.SelfPubkey == evt.Pubkey {
		s.buzzPeer.MessageMan.BcastProfile(evt)
		// update local profile
		glo_val.CurrentProfileEvt = evt
	}

	w.WriteJson(&GeneralResp{
		"SUCCESS",
	})
}

func (s *ApiServer) setOrUpdateFollowList(w rest.ResponseWriter, input *schema.Np2pEventForREST) {
	if input.Tags == nil {
		rest.Error(w, "Tags is null", http.StatusBadRequest)
		return
	}

	evt := schema.NewNp2pEventFromREST(input)
	if *glo_val.SelfPubkey == evt.Pubkey {
		s.buzzPeer.MessageMan.DataMan.StoreEvent(evt)
		// update local profile
		glo_val.CurrentFollowListEvt = evt
	}

	w.WriteJson(&GeneralResp{
		"SUCCESS",
	})
}

func (s *ApiServer) sendReaction(w rest.ResponseWriter, input *schema.Np2pEventForREST) {
	evt := schema.NewNp2pEventFromREST(input)
	err := s.buzzPeer.MessageMan.UnicastEventData(string((*(schema.FindFirstSpecifiedTag(&evt.Tags, "p")))[1]), evt)
	if err != nil && string((*(schema.FindFirstSpecifiedTag(&evt.Tags, "p")))[1]) != glo_val.SelfPubkeyStr {
		// destination server is offline
		// so add event to retry queue
		// except destination is myself case
		s.buzzPeer.MessageMan.DataMan.AddReSendNeededEvent([]uint64{np2p_util.Get6ByteUint64FromHexPubKeyStr(string((*(schema.FindFirstSpecifiedTag(&evt.Tags, "p")))[0]))}, evt, true)
		fmt.Println(string((*(schema.FindFirstSpecifiedTag(&evt.Tags, "p")))[1]))
		fmt.Println(err)
	}

	// stored for myself
	// if destination server is offline, this event will be sent again
	s.buzzPeer.MessageMan.DataMan.StoreEvent(evt)

	w.WriteJson(&EventsResp{})
}

func (s *ApiServer) reqHandler(w rest.ResponseWriter, req *rest.Request) {
	input := schema.Np2pReqForREST{}
	err := req.DecodeJsonPayload(&input)

	//fmt.Println("reqHandler")
	//fmt.Println(input.Tag)
	if err != nil {
		fmt.Println(err)
		rest.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if input.Kinds == nil || len(input.Kinds) == 0 {
		//rest.Error(w, "Kinds is needed", http.StatusBadRequest)
		//return

		// for supporting Nostr clients
		w.WriteJson(&EventsResp{
			Events: []schema.Np2pEventForREST{},
		})
		return
	}

	// TODO: need to check Created_at and Sig for authorizaton (ApiServer::reqHandler)
	//       accept only when ((currentTime - Created_at) < 10sec)

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	// TODO: need to implement each kind and other fliter condition request handling (ApiServer::reqHandler)
	if slices.Contains(input.Kinds, core.KIND_REQ_SHARE_EVT_DATA) {
		s.getEvents(w, &input)
	} else if slices.Contains(input.Kinds, core.KIND_REQ_POST) {
		s.getPost(w, &input)
	} else if slices.Contains(input.Kinds, core.KIND_REQ_PROFILE) {
		s.getProfile(w, &input)
	} else if slices.Contains(input.Kinds, core.KIND_REQ_FOLLOW_LIST) {
		s.getFollowList(w, &input)
	} else {

		w.WriteJson(&EventsResp{
			Events: []schema.Np2pEventForREST{},
		})
		return
	}
}

// RESTRICTION: only one ID and author is supported
func (s *ApiServer) getPost(w rest.ResponseWriter, input *schema.Np2pReqForREST) {
	if input.Ids == nil || len(input.Ids) == 0 || input.Authors == nil || len(input.Authors) == 0 {
		rest.Error(w, "Ids and Authors are needed", http.StatusBadRequest)
		return
	}

	tgtEvtId := np2p_util.StrTo32BytesArr(input.Ids[0])
	shortPkey := np2p_util.GetUint64FromHexPubKeyStr(input.Authors[0])
	gotEvt, ok := s.buzzPeer.MessageMan.DataMan.GetEventById(tgtEvtId)

	if ok {
		// found at local
		w.WriteJson(&EventsResp{Events: []schema.Np2pEventForREST{*schema.NewNp2pEventForREST(gotEvt)}})
	} else {
		// post data will be included on response of "getEvents"
		w.WriteJson(&EventsResp{Events: []schema.Np2pEventForREST{}})
		// request post data for future
		s.buzzPeer.MessageMan.UnicastPostReq(shortPkey, tgtEvtId)
	}
}

func (s *ApiServer) getProfile(w rest.ResponseWriter, input *schema.Np2pReqForREST) {
	shortPkey := np2p_util.GetUint64FromHexPubKeyStr(input.Authors[0])
	profEvt := s.buzzPeer.MessageMan.DataMan.GetProfileLocal(shortPkey)

	if profEvt != nil {
		w.WriteJson(&EventsResp{Events: []schema.Np2pEventForREST{*schema.NewNp2pEventForREST(profEvt)}})
	} else {
		// profile data will be included on response of "getEvents"
		w.WriteJson(&EventsResp{Events: []schema.Np2pEventForREST{}})
		// request profile data for future
		s.buzzPeer.MessageMan.UnicastProfileReq(shortPkey)
	}
}

func (s *ApiServer) getFollowList(w rest.ResponseWriter, input *schema.Np2pReqForREST) {
	shortPkey := np2p_util.GetUint64FromHexPubKeyStr(input.Authors[0])
	fListEvt := s.buzzPeer.MessageMan.DataMan.GetFollowListLocal(shortPkey)

	if fListEvt != nil {
		w.WriteJson(&EventsResp{Events: []schema.Np2pEventForREST{*schema.NewNp2pEventForREST(fListEvt)}})
	} else {
		// follow list data will be included on response of "getEvents"
		w.WriteJson(&EventsResp{Events: []schema.Np2pEventForREST{}})
		// request profile data for future
		s.buzzPeer.MessageMan.UnicastFollowListReq(shortPkey)
	}
}

// input.Simce == -1 && input.Until == -1 => specified only input.Limit
func (s *ApiServer) getEvents(w rest.ResponseWriter, input *schema.Np2pReqForREST) {
	// for supporting Nostr clients
	isPeriodSpecified := true
	if input.Since == 0 {
		input.Since = -1
		input.Until = -1
		// limit must be specified!
		isPeriodSpecified = false
	}
	if input.Until == 0 {
		input.Until = math.MaxInt64
	}
	if input.Limit == 0 {
		input.Limit = -1
	}

	events := s.buzzPeer.MessageMan.DataMan.GetLatestEvents(input.Since, input.Until, input.Limit)

	// for supporting Nostr clients
	// limit 50
	if !isPeriodSpecified && len(*events) > 50 {
		*events = (*events)[len(*events)-50:]
	}

	retEvents := make([]schema.Np2pEventForREST, 0)

	for _, evt := range *events {
		retEvents = append(retEvents, *schema.NewNp2pEventForREST(evt))
	}

	w.WriteJson(&EventsResp{
		Events: retEvents,
	})
}

// TODO: TEMPORAL IMPL
func (s *ApiServer) gatherData(w rest.ResponseWriter, req *rest.Request) {
	input := NoArgReq{}
	err := req.DecodeJsonPayload(&input)

	if err != nil {
		fmt.Println(err)
		rest.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.buzzPeer.MessageMan.BcastShareEvtDataReq()

	w.WriteJson(&GeneralResp{
		"SUCCESS",
	})
}

func (s *ApiServer) LaunchAPIServer(addrStr string) {
	api := rest.NewApi()

	// the Middleware stack
	//api.Use(rest.DefaultDevStack...)
	api.Use(
		//&rest.AccessLogApacheMiddleware{},
		&rest.TimerMiddleware{},
		&rest.RecorderMiddleware{},
		&rest.PoweredByMiddleware{},
		&rest.RecoverMiddleware{
			EnableResponseStackTrace: true,
		},
		&rest.JsonIndentMiddleware{},
		&rest.ContentTypeCheckerMiddleware{},
	)
	api.Use(&rest.JsonpMiddleware{
		CallbackNameKey: "cb",
	})
	api.Use(&rest.CorsMiddleware{
		RejectNonCorsRequests: false,
		OriginValidator: func(origin string, request *rest.Request) bool {
			return true
		},
		AllowedMethods:                []string{"POST", "OPTIONS"},
		AllowedHeaders:                []string{"Accept", "content-type", "Access-Control-Request-Headers", "Access-Control-Request-Method", "Origin", "Referer", "User-Agent"},
		AccessControlAllowCredentials: true,
		AccessControlMaxAge:           3600,
	})

	router, err := rest.MakeRouter(
		&rest.Route{"POST", "/publish", s.publishHandler},
		&rest.Route{"POST", "/req", s.reqHandler},
	)
	if err != nil {
		log.Fatal(err)
	}
	api.SetApp(router)

	log.Printf("Server started")
	if glo_val.IsEnabledSSL {
		log.Fatal(http.ListenAndServeTLS(
			addrStr,
			"cert.pem",
			"privkey.pem",
			api.MakeHandler(),
		))
	} else {
		log.Fatal(http.ListenAndServe(
			addrStr,
			api.MakeHandler(),
		))
	}
}
