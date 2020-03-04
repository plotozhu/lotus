package transp2p

/***

 */
import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/bluele/gcache"

	"github.com/smallnest/rpcx/log"

	util "github.com/filecoin-project/lotus/chain/pnyx/rtutil"
	"github.com/filecoin-project/lotus/chain/pnyx/transpp"
	"github.com/fxamacker/cbor"

	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-peer"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
)

//RouteIntf 路由管理器接口
type RouteIntf interface {
	GetRoutes(dest peer.ID) ([]*RouteTableItem, error)
	GetBestRoute(dest peer.ID) (*RouteTableItem, error)
	UpdateRoute(dest, next peer.ID, ttl uint8)
	UpdateNeighbour(next peer.ID, breakdown bool)
	Attach(RouteObserver)
	Dettach(RouteObserver)
}

//P2PObserver observer interface for TransP2P
type P2PObserver interface {
	PeerConnect(peerID peer.ID)
	PeerDisconnect(peerID peer.ID)
	RouteItemUpdated(dst peer.ID, routeItems []*RouteTableItem)
	//GetID is used to identify observer, same id means same observer
	GetID() string
}

const (
	maxPendDataCnt          = 1024
	maxFindCache            = 1024
	handleRoute             = "R/CMD"
	handlePingPong          = "R/PING"
	handleData              = "R/DATA"
	cmdFind           uint8 = 0x01
	cmdFindResp       uint8 = 0x02
	cmdSendData       uint8 = 0x10
	cmdDataResp       uint8 = 0x11
	cmdPing           uint8 = 0x01
	cmdPong           uint8 = 0x02
	maxSendingPacket        = 6400
	maxResponsedCnt         = 1024 * 100
	maxNeighbours           = 128
	pingInterval            = 20 * time.Second
	statePinged             = 1
	statePonged             = 2
	statePingReceived       = 3
	sendDataTimeout         = 10 * time.Minute
	HashLen                 = 32
)

type PingData struct {
	Type uint8
}
type PingPongState struct {
	timer *time.Timer
	state int
}
type PendingData struct {
	Dst     peer.ID
	Alpha   uint8
	OTtl    uint8
	Data    []byte
	Wait    chan error
	timeout time.Duration
}
type DataHandle func(data []byte)

//FindRouteReq map[req_sender]ttl_value
type FindRouteReq map[peer.ID]uint8

//TransP2P
type TransP2P struct {
	ppSvr          *transpp.TransPushPullService
	pendDataCache  gcache.Cache //those waiting for routing
	self           peer.ID
	pstore         peerstore.Peerstore
	buckets        map[int](map[peer.ID]bool)
	saturation     int
	minBucketSize  int
	bucketsLock    sync.Mutex
	peerStoreHash  [HashLen]byte
	dataChannel    chan *PendingData
	pingChannel    chan *PingData
	routeTab       RouteIntf
	currentFd      uint64
	lock           sync.Mutex
	waitingForResp gcache.Cache
	responsedCache gcache.Cache
	handle         DataHandle
	findRouteCache gcache.Cache
	pingpongCache  gcache.Cache

	ctx          context.Context
	ppCacheMutex sync.Mutex
	observers    gcache.Cache
}

// MsgFindRoute 是发现消息
type MsgFindRoute struct {
	Cmd      uint8
	Dst      peer.ID
	Src      peer.ID
	Alpha    uint8
	OTtl     uint8
	Ttl      uint8
	Outbound []byte
	Rpts     []byte //used for token consume,current is nil
}

//MsgDataTrans msg for data transfer
type MsgDataTrans struct {
	Cmd  uint8
	Dst  peer.ID
	Src  peer.ID
	OTtl uint8
	Ttl  uint8
	Fd   uint64
	Data []byte
	Rpts []byte //used for token consume,current is nil
}

//CreateTransP2P create a P2P transfer service
func NewTransP2P(ctx context.Context, ahost host.Host, routeTab RouteIntf, handle DataHandle) (*TransP2P, error) {
	var err error
	if routeTab == nil || reflect.ValueOf(routeTab).IsNil() {
		routeTab, err = CreateRouter()
		if err != nil {
			return nil, err
		}
	}
	transInst := TransP2P{
		ppSvr:          transpp.NewTransPushPullTransfer(ctx, ahost),
		pendDataCache:  gcache.New(maxPendDataCnt).LRU().Build(), // 128, 10*time.Minute),
		self:           ahost.ID(),
		pstore:         ahost.Peerstore(),
		routeTab:       routeTab,
		dataChannel:    make(chan *PendingData, maxSendingPacket),
		pingChannel:    make(chan *PingData, maxSendingPacket),
		responsedCache: gcache.New(maxResponsedCnt).LRU().Build(), // 128, 10*time.Minute),
		findRouteCache: gcache.New(maxFindCache).LRU().Build(),    // 128, 10*time.Minute),
		observers:      gcache.New(100).LRU().Build(),
		currentFd:      0,
		handle:         handle,
		ctx:            ctx,
		minBucketSize:  2,
		saturation:     0,
	}
	transInst.waitingForResp = gcache.New(maxSendingPacket).LRU().EvictedFunc(transInst.onSendTimeout).Build()
	transInst.pingpongCache = gcache.New(maxNeighbours).LRU().Build()
	transInst.ppSvr.RegisterHandle(handlePingPong, transInst.procPingPong, nil)
	transInst.ppSvr.RegisterHandle(handleRoute, transInst.procRouteInfo, nil)
	transInst.ppSvr.RegisterHandle(handleData, transInst.procDataArrival, nil)
	go transInst.run(ctx)
	return &transInst, nil
}

func (tp *TransP2P) run(ctx context.Context) {
	//go tp.startPingService()
	for {
		select {
		case packet := <-tp.dataChannel:
			tp.createAndRelay(packet)
		case <-ctx.Done():
			return
		}
	}
}

func (tp *TransP2P) buildBucket() {
	bytes, err := cbor.Marshal(tp.pstore.Peers(), cbor.EncOptions{})
	if err != nil {

		log.Errorf("%v error in creating hash ", tp.self)
	}
	hash := util.Hash(bytes)
	tempHash := [HashLen]byte{}
	copy(tempHash[:], hash[:32])
	if tempHash == tp.peerStoreHash {
		return
	}
	copy(tp.peerStoreHash[:], hash[:32])
	tp.bucketsLock.Lock()
	defer tp.bucketsLock.Unlock()
	//reset tp.buckets
	tp.buckets = make(map[int](map[peer.ID]bool))
	for _, peerID := range tp.pstore.Peers() {
		if strings.Compare(string(tp.self), string(peerID)) == 0 {
			continue
		}
		dist := util.Dist(peerID, tp.self)
		nodes := tp.buckets[dist]
		if nodes == nil {
			tp.buckets[dist] = make(map[peer.ID]bool)
			tp.buckets[dist][peerID] = true
		} else {
			tp.buckets[dist][peerID] = true
		}
	}
	pententialSat := 0
	//calculate saturation
	for i := 0; i < util.ADDR_LEN; i++ {
		if len(tp.buckets[i]) >= tp.minBucketSize {
			pententialSat = i
		} else {
			break
		}
	}
	nodesAboveSat := 0
	for i := pententialSat; i < util.ADDR_LEN; i++ {
		nodesAboveSat += len(tp.buckets[i])
		if nodesAboveSat >= tp.minBucketSize {
			break
		}
	}
	if nodesAboveSat < tp.minBucketSize {
		if pententialSat > 0 {
			pententialSat--
		}
	}
	tp.saturation = pententialSat
	log.Infof("%v create bucket ok with saturation:%v", tp.self, tp.saturation)
}

//SetDataHandle for this service
func (tp *TransP2P) SetDataHandle(handle DataHandle) {
	tp.handle = handle
}

//SetDataHandle for this service
func (tp *TransP2P) SetMinBucketSize(size int) {
	tp.minBucketSize = size
}

//RouteItemUpdated is called by routetab
func (tp *TransP2P) RouteItemUpdated(dst peer.ID, items []*RouteTableItem) {
	for _, observer := range tp.observers.GetALL(false) {
		go func(dst peer.ID, items []*RouteTableItem, observer P2PObserver) {
			observer.RouteItemUpdated(dst, items)
		}(dst, items, observer.(P2PObserver))
	}
}

//Attach add new observer to watchlist
func (tp *TransP2P) Attach(observer P2PObserver) {
	firstAdd := false
	if tp.observers.Len(false) == 0 {
		firstAdd = true
	}
	tp.observers.Set(observer.GetID(), observer)
	if firstAdd {
		tp.routeTab.Attach(tp)
	}
}

//Dettach remove observer from watchlist
func (tp *TransP2P) Dettach(obsIndex string) {
	shouldRemove := false
	if tp.observers.Len(false) == 1 && tp.observers.Has(obsIndex) {
		shouldRemove = true
	}
	tp.observers.Remove(obsIndex)
	if shouldRemove {
		tp.routeTab.Dettach(tp)
	}
}
func (tp *TransP2P) resetPingWorker(ctx context.Context, peerID peer.ID, state int) {
	tp.ppCacheMutex.Lock()
	defer tp.ppCacheMutex.Unlock()
	pp, err := tp.pingpongCache.Get(peerID)
	var ppState *PingPongState

	rand.Seed(time.Now().UnixNano())
	sendInterval := pingInterval + time.Duration(rand.Intn(5000))*time.Millisecond

	if err == nil {
		ppState = pp.(*PingPongState)
		ppState.timer.Reset(sendInterval)
		ppState.state = state
	} else {
		ppState = &PingPongState{
			state: state,
			timer: time.NewTimer(time.Minute),
		}
		ppState.timer.Reset(sendInterval)
		ppState.state = state
		tp.pingpongCache.Set(peerID, ppState)
		for _, observer := range tp.observers.GetALL(false) {
			go func(dst peer.ID, observer P2PObserver) {
				observer.PeerConnect(dst)
			}(peerID, observer.(P2PObserver))

		}
		go func(sendInterval time.Duration, pp *PingPongState) {
			for {
				select {
				case <-pp.timer.C:
					if ppState.state == statePinged {
						//ping but no pong response,remove it
						tp.routeTab.UpdateNeighbour(peerID, true)
						//DONOT RESTART timer and remove from cache, sync must be considered
						tp.ppCacheMutex.Lock()
						defer tp.ppCacheMutex.Unlock()
						tp.pingpongCache.Remove(peerID)
						for _, observer := range tp.observers.GetALL(false) {
							go func(dst peer.ID, observer P2PObserver) {
								observer.PeerDisconnect(dst)
							}(peerID, observer.(P2PObserver))
						}
						return
					}
					tp.sendPingPongTo(peerID, cmdPing)
					tp.resetPingWorker(tp.ctx, peerID, statePinged)
					pp.timer.Reset(sendInterval)

				case <-ctx.Done():
					pp.timer.Stop()
					return
				}

			}
		}(sendInterval, ppState)
	}

}
func (tp *TransP2P) startPingService() {
	if tp.pstore != nil && !reflect.ValueOf(tp.pstore).IsNil() {
		<-time.NewTimer(5 * time.Second).C
		for _, peerID := range tp.pstore.Peers() {
			tp.sendPingPongTo(peerID, cmdPing)
			tp.resetPingWorker(tp.ctx, peerID, statePinged)

		}
	}

}
func (tp *TransP2P) sendPingPongTo(peerID peer.ID, cmd uint8) {
	msg := PingData{
		Type: cmd,
	}
	pingMsg, err := cbor.Marshal(msg, cbor.CanonicalEncOptions())
	if err == nil {
		tp.ppSvr.SendToPeer(peerID, handlePingPong, pingMsg)

	} else {
		log.Infof("Marshal message failed:%v", msg)
	}
}

func (tp *TransP2P) procPingPong(lastHop peer.ID, data []byte, pInfo interface{}) error {
	var msg PingData
	err := cbor.Unmarshal(data, msg)
	if err != nil {
		return err
	}
	if msg.Type == cmdPing {
		tp.sendPingPongTo(lastHop, cmdPong)
		tp.resetPingWorker(tp.ctx, lastHop, statePingReceived)

	} else if msg.Type == cmdPong {
		tp.resetPingWorker(tp.ctx, lastHop, statePonged)
	} else {
		return fmt.Errorf("ping pong cmd not exist:%v", msg.Type)
	}

	tp.routeTab.UpdateNeighbour(lastHop, false)

	return nil
}

/**
 *  these two should be combined to one function
 */
func (tp *TransP2P) procRouteInfo(lastHop peer.ID, data []byte, pInfo interface{}) error {
	//	log.Infof("procRouteInfo my:%v, lastHop:%v\n", tp.self.Pretty(), lastHop.Pretty())
	// ignore requirement if this is rewind

	var msg = &MsgFindRoute{}
	err := cbor.Unmarshal(data, msg)
	if err != nil {
		log.Errorf("proc Route Info error :%v, %v", err, data)
		return err
	}

	//update route table and process pending data if exists
	//log.Infof("updateRoute,this:%v,dst:%v,next:%v,ttl:%v", tp.self, msg.Src, lastHop, msg.OTtl-msg.Ttl+1)

	tp.routeTab.UpdateRoute(msg.Src, lastHop, msg.OTtl-msg.Ttl+1)
	go tp.reponsePendingFindReq(lastHop, msg)
	go tp.procPending(msg.Src)
	switch msg.Cmd {
	case cmdFind:
		return tp.procFindReq(lastHop, msg)
	case cmdFindResp:
		return tp.procFindResp(lastHop, msg)
	}
	log.Errorf("cmd not exist:%v", msg.Cmd)
	return fmt.Errorf("cmd not exist:%v", msg.Cmd)
}

// when data is arrival, response when data is to myself, or relay it, token should be consumed in the future
func (tp *TransP2P) procDataArrival(lastHop peer.ID, data []byte, pInfo interface{}) error {
	var msg = &MsgDataTrans{}
	err := cbor.Unmarshal(data, msg)
	if err != nil {
		log.Errorf("Error unmarshal data:%v", err)
		return err
	}
	log.Infof("dataArrived,this:%v,dst:%v,next:%v,ttl:%v", tp.self, msg.Src, lastHop, msg.OTtl-msg.Ttl+1)
	//update route table and process pending data if exists
	tp.routeTab.UpdateRoute(msg.Src, lastHop, msg.OTtl-msg.Ttl+1)
	go tp.procPending(msg.Src)
	switch msg.Cmd {
	case cmdSendData:
		return tp.procDataSend(lastHop, msg)
	case cmdDataResp:
		return tp.procDataResp(msg)
	}
	return fmt.Errorf("cmd not exist:%v", msg.Cmd)

}

//for those who asked us to find route and waiting for response
func (tp *TransP2P) reponsePendingFindReq(nextHop peer.ID, rawMsg *MsgFindRoute) {
	pendingItem, err := tp.findRouteCache.Get(rawMsg.Src)
	if err == nil {
		for reqSender, _ := range pendingItem.(FindRouteReq) {
			msg := MsgFindRoute{
				Cmd:   cmdFindResp,
				Dst:   rawMsg.Src,
				Src:   rawMsg.Dst,
				Alpha: alpha,
				OTtl:  rawMsg.OTtl,
				Ttl:   rawMsg.Ttl - 1,
			}
			data, err := cbor.Marshal(&msg, cbor.EncOptions{})
			if err == nil {
				tp.ppSvr.SendToPeer(reqSender, handleRoute, data)
			}
		}
	}
}
func (tp *TransP2P) procFindReq(lastHop peer.ID, msg *MsgFindRoute) error {
	//route is updated before calling this
	//I am the destination, send findResp
	if strings.Compare(string(msg.Dst), string(tp.self)) == 0 {
		//send response

		msg := MsgFindRoute{
			Cmd:   cmdFindResp,
			Dst:   msg.Src,
			Src:   tp.self,
			Alpha: alpha,
			OTtl:  msg.OTtl - msg.Ttl + 2,
			Ttl:   msg.OTtl - msg.Ttl + 2,
		}
		data, err := cbor.Marshal(&msg, cbor.EncOptions{})
		if err != nil {
			return err
		}
		tp.ppSvr.SendToPeer(lastHop, handleRoute, data)
		return nil
	}
	//try to find a valid one from routeTab,
	item, err := tp.routeTab.GetBestRoute(msg.Dst)
	if err == nil {
		msg := MsgFindRoute{
			Cmd:   cmdFindResp,
			Dst:   msg.Src,
			Src:   msg.Dst,
			Alpha: alpha,
			OTtl:  msg.OTtl - msg.Ttl + 2,
			Ttl:   msg.OTtl - msg.Ttl + 2 - item.ttl,
		}
		data, err := cbor.Marshal(&msg, cbor.EncOptions{})
		if err == nil {
			tp.ppSvr.SendToPeer(lastHop, handleRoute, data)
			return nil
		}

	}
	//record this find request
	request, err := tp.findRouteCache.Get(msg.Dst)
	if err == nil {
		result := request.(FindRouteReq)
		(result)[lastHop] = (msg.OTtl - msg.Ttl - 1)
		tp.findRouteCache.SetWithExpire(msg.Dst, result, 5*time.Minute)
		return nil

	}

	//if ttl > 0 relay to nearer nodes
	if msg.Ttl > 0 {
		msg.Ttl--
		pendingReq := make(FindRouteReq)
		pendingReq[lastHop] = (msg.OTtl - msg.Ttl)
		tp.findRouteCache.SetWithExpire(msg.Dst, pendingReq, 5*time.Minute)
		return tp.sendRouteDataToNearer(msg)
	}
	return fmt.Errorf("ttl out")

}
func (tp *TransP2P) procFindResp(lastHop peer.ID, msg *MsgFindRoute) error {
	//neighbour route is updated before calling this
	// I am the destination
	if strings.Compare(string(msg.Dst), string(tp.self)) == 0 {

		return nil
	}
	//if ttl > 0 relay
	if msg.Ttl > 0 {
		msg.Ttl--
	} else {
		return fmt.Errorf("ttl out")
	}
	return tp.sendRouteDataToNearer(msg)
}

func (tp *TransP2P) procDataSend(lastHop peer.ID, msg *MsgDataTrans) error {
	// if this data is sent to me
	log.Infof("%v received data to %v", tp.self, msg.Dst)
	if strings.Compare(string(msg.Dst), string(tp.self)) == 0 {
		log.Info("%v Receive Data from %v with FD:%v", tp.self, msg.Src, msg.Fd)
		//data may send to me multitimes from different route, Only one response will be sent
		responsed := fmt.Sprintf("%v:%v", msg.Src, msg.Fd)
		_, ok := tp.responsedCache.Get(responsed)
		if ok == nil {
			return nil
		}
		tp.responsedCache.SetWithExpire(responsed, true, expireTime)
		respMsg := &MsgDataTrans{Cmd: cmdDataResp, Dst: msg.Src, Src: tp.self, OTtl: msg.OTtl - msg.Ttl + 2, Ttl: msg.OTtl - msg.Ttl + 2, Fd: msg.Fd}
		if tp.handle != nil {
			go tp.handle(msg.Data)
		}

		data, err := cbor.Marshal(respMsg, cbor.EncOptions{})
		if err != nil {
			return err
		}

		tp.ppSvr.SendToPeer(lastHop, handleData, data)
		return nil
	}
	//should reply to next
	//if ttl > 0 relay
	if msg.Ttl > 0 {
		msg.Ttl--
	} else {
		return fmt.Errorf("ttl out")
	}
	//TODO calculation data and token
	log.Infof("%v received data and relaying to %v", tp.self, msg.Dst)
	return tp.relayData(msg, true)
}
func (tp *TransP2P) procDataResp(msg *MsgDataTrans) error {
	if strings.Compare(string(msg.Dst), string(tp.self)) == 0 {
		// finding who is wait for this
		result, ok := tp.waitingForResp.Get(msg.Fd)
		if ok == nil {
			if !reflect.ValueOf(result).IsNil() {
				val := result.(chan error)
				if val != nil {
					go func() {
						val <- nil
					}()
				}
			}

		}
		tp.waitingForResp.Remove(msg.Fd)
	}
	//if ttl > 0 relay
	if msg.Ttl > 0 {
		msg.Ttl--
	} else {
		return fmt.Errorf("ttl out")
	}

	//TODO calculation data and token,  and resign a Rpts to msg.Rpts
	return tp.relayData(msg, true)
}

// procPending is called when route item to dst has been established
func (tp *TransP2P) procPending(dst peer.ID) {
	tp.lock.Lock()
	defer tp.lock.Unlock()
	//	log.Infof("%v try to retract  relaying data:%v", tp.self, dst)
	pendings, ok := tp.pendDataCache.Get(dst)
	if ok == nil {

		//	log.Infof("Data extracted")
		result := pendings.([]*MsgDataTrans)
		for _, msg := range result {
			tp.relayData(msg, false)
		}
		tp.pendDataCache.Remove(dst)

	}
}

/**
 *	relay data only invoked when there should be
 *  multiple data retrieve is eliminated on PushPull Service
 */
func (tp *TransP2P) relayData(msg *MsgDataTrans, autoFind bool) error {
	peers, err := tp.routeTab.GetRoutes(msg.Dst)
	log.Infof("%v relaying Data from %v  to %v with FD:%v", tp.self, msg.Src, msg.Dst, msg.Fd)
	if len(peers) == 0 || err != nil {
		//store data and to find next?
		//now simple discard message
		if autoFind {

			tp.pendDataCache.SetWithExpire(msg.Dst, []*MsgDataTrans{msg}, sendDataTimeout)
			tp.FindRoute(msg.Dst, msg.Ttl, alpha, nil)
			log.Infof("%v pending  relaying data:%v", tp.self, msg.Dst)
		} else {
			log.Infof("%v discard  relaying data:%v", tp.self, msg.Dst)
		}

	} else {
		fmt.Printf("route table is:{")
		for _, info := range peers {
			fmt.Printf("%v:%v,", info.next.Pretty(), info.ttl)
		}
		fmt.Printf("}\n")
		data, err := cbor.Marshal(msg, cbor.EncOptions{})
		if err == nil {
			for _, nextPeer := range peers {
				log.Infof("Send Data to : %v", nextPeer.next)
				tp.ppSvr.SendToPeer(nextPeer.next, handleData, data)
			}
		} else {
			log.Errorf("%v error in relaying data %v", tp.self, err)
		}

	}
	return nil
}

//AddPeer TODO manage by route distance algorithm
func (tp *TransP2P) AddPeer(peerID peer.ID) {

}

//RemovePeer TODO manage peers by route distance algorithm
func (tp *TransP2P) RemovePeer(peer.ID, peer.ID) {

}

//DistInfo to store dist and peer pair
type DistInfo struct {
	dist uint64
	peer *peer.ID
}

//FindRoute Send to find a route command, ttl is the max-hop for find data and alpha is fan-out, Oob is optional and should be less than 64 bytes
// in the future, token should be provided
func (tp *TransP2P) FindRoute(targetPeer peer.ID, ttl uint8, alpha uint8, outbound []byte) error {

	log.Infof("%v Find route %v", tp.self, targetPeer)
	if strings.Compare(string(targetPeer), string(tp.self)) == 0 {
		return nil
	}

	msg := MsgFindRoute{
		Cmd:      cmdFind,
		Dst:      targetPeer,
		Src:      tp.self,
		Alpha:    alpha,
		OTtl:     ttl,
		Ttl:      ttl,
		Outbound: outbound,
	}
	return tp.sendRouteDataToNearer(&msg)
}

func (tp *TransP2P) sendRouteDataToNearer(msg *MsgFindRoute) error {

	//Get \alpha nearer peerid and send data to
	//this is with very low effiecny and only used for evaluation ,a tree should be  used to find nodes as fast as possible

	tp.buildBucket()
	target := make([]*peer.ID, 0)

	bucketIndex := util.Dist(tp.self, msg.Dst)
	log.Infof("target %v in %v's bucket %v", peer.IDHexEncode(msg.Dst), peer.IDHexEncode(tp.self), bucketIndex)
	tp.bucketsLock.Lock()
	ignoredPeersIntf, getPendErr := tp.findRouteCache.Get(msg.Dst)
	var ignoredPeers = make(FindRouteReq)
	if getPendErr == nil {
		ignoredPeers = ignoredPeersIntf.(FindRouteReq)
	}

	defer tp.bucketsLock.Lock()
	if bucketIndex < tp.saturation {
		for key, _ := range tp.buckets[bucketIndex] {

			_, exists := (ignoredPeers)[key]
			if !exists {
				target = append(target, &key)
			}
		}
	} else {
		for i := tp.saturation; i < util.ADDR_LEN; i++ {
			for key, _ := range tp.buckets[i] {
				_, exists := (ignoredPeers)[key]
				if !exists {
					newkey, _ := peer.IDHexDecode(peer.IDHexEncode(key))
					target = append(target, &newkey)
				}
			}
		}
	}
	if len(target) > int(alpha) {
		target = target[:alpha]
	}
	data, err := cbor.Marshal(msg, cbor.EncOptions{})
	if err != nil {
		return err
	}
	if len(target) == 0 {
		//log.Errorf("\nNO more nearer neighbour found for %v,myID: %v, myDist:%v maps:%v\n", msg.Dst, tp.self, myDist, distsmap)
		return nil
	}
	tp.ppSvr.SendToPeers(target, handleRoute, data)
	//log.Infof("\nFindRoute: this:%v,dst:%v\t,next:%v,totalNeighbour:%v\n", tp.self, msg.Dst, target, len(tp.pstore.Peers()))
	return nil
}

//SendData Send to find a route command, ttl is the max-hop for find data and alpha is fan-out, outbound is optional and should be less than 64 bytes
// in the future, token should be provided
func (tp *TransP2P) SendData(targetPeer peer.ID, ttl uint8, alpha uint8, data []byte, timeout time.Duration, ret chan error) error {

	if strings.Compare(string(targetPeer), string(tp.self)) == 0 {
		return nil
	}

	pending := PendingData{Dst: targetPeer, Alpha: alpha, OTtl: ttl, Data: data, Wait: ret, timeout: timeout}
	go func() {
		tp.dataChannel <- &pending
	}()
	return nil
}

func (tp *TransP2P) createAndRelay(pending *PendingData) error {
	tp.currentFd++
	msg := &MsgDataTrans{
		Cmd:  cmdSendData,
		Dst:  pending.Dst,
		Src:  tp.self,
		OTtl: pending.OTtl,
		Ttl:  pending.OTtl,
		Fd:   tp.currentFd,
		Data: pending.Data,
		//Rpts []byte //used for token consume,current is nil
	}
	if pending.Wait != nil {
		tp.waitingForResp.SetWithExpire(msg.Fd, pending.Wait, pending.timeout)
	}
	return tp.relayData(msg, true)

}

func (tp *TransP2P) onSendTimeout(key interface{}, value interface{}) {
	result := value.(chan error)
	go func(ret chan error) {
		ret <- fmt.Errorf("timeout")
	}(result)
}
