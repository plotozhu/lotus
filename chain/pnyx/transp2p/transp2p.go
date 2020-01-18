package transp2p

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/filecoin-project/lotus/chain/pnyx/transpp"
	"github.com/filecoin-project/lotus/chain/pnyx/util"
	"github.com/fxamacker/cbor"
	"github.com/golang/groupcache/lru"
	"github.com/libp2p/go-libp2p-core/peer"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
)

const (
	maxPendDataCnt   = 1024
	handleRoute      = "ROUTE/CMD"
	handleData       = "ROUTE/DATA"
	cmdFind          = 0x01
	cmdFindResp      = 0x02
	cmdSendData      = 0x10
	cmdDataResp      = 0x11
	maxSendingPacket = 64
	maxResponsedCnt  = 1024 * 100
)

type PendingData struct {
	dst    peer.ID
	alpha  uint8
	orgTTL uint8
	data   []byte
	wait   chan error
}
type DataHandle func(data []byte)
type TransP2P struct {
	ppSvr          *transpp.TransPushPullService
	pendingCache   *lru.Cache //those waiting for routing
	self           peer.ID
	pstore         peerstore.Peerstore
	buckets        sync.Map
	dataChannel    chan *PendingData
	routeTab       *RouteInfo
	currentFd      uint64
	lock           sync.Mutex
	waitingForResp sync.Map
	responsedCache *lru.Cache
	handle         DataHandle
}
type MsgFindRoute struct {
	cmd      uint8
	dst      peer.ID
	src      peer.ID
	alpha    uint8
	orgTTL   uint8
	ttl      uint8
	outbound []byte
	receipts []byte //used for token consume,current is nil
}

//MsgDataTrans msg for data transfer
type MsgDataTrans struct {
	cmd      uint8
	dst      peer.ID
	src      peer.ID
	orgTTL   uint8
	ttl      uint8
	fd       uint64
	data     []byte
	receipts []byte //used for token consume,current is nil
}

//CreateTransP2P create a P2P transfer service
func CreateTransP2P(ctx context.Context, ppSvr *transpp.TransPushPullService, self peer.ID, pstore peerstore.Peerstore, routeTab *RouteInfo, handle DataHandle) *TransP2P {

	transInst := TransP2P{
		ppSvr:          ppSvr,
		pendingCache:   lru.New(maxPendDataCnt),
		self:           self,
		pstore:         pstore,
		routeTab:       routeTab,
		dataChannel:    make(chan *PendingData, maxSendingPacket),
		responsedCache: lru.New(maxResponsedCnt),
		currentFd:      0,
		handle:         handle,
	}
	ppSvr.RegisterHandle(handleRoute, transInst.procRouteInfo)
	ppSvr.RegisterHandle(handleData, transInst.procDataArrival)
	return &transInst
}
func (tp *TransP2P) run(ctx context.Context) {
	for {
		select {
		case packet := <-tp.dataChannel:
			tp.createAndRelay(packet)
		case <-ctx.Done():
			return
		}
	}
}

/**
 *  these two should be combined to one function
 */
func (tp *TransP2P) procRouteInfo(lastHop peer.ID, data []byte) error {
	var msg MsgFindRoute
	err := cbor.Unmarshal(data, msg)
	if err != nil {
		return err
	}

	//update route table and process pending data if exists
	tp.routeTab.UpdateRoute(msg.src, lastHop, msg.orgTTL-msg.ttl)
	go tp.procPending(msg.src)
	switch msg.cmd {
	case cmdFind:
		return tp.procFindReq(lastHop, &msg)
	case cmdFindResp:
		return tp.procFindResp(&msg)
	}
	return fmt.Errorf("cmd not exist:%v", msg.cmd)
}

// when data is arrival, response when data is to myself, or relay it, token should be consumed in the future
func (tp *TransP2P) procDataArrival(lastHop peer.ID, data []byte) error {
	var msg MsgDataTrans
	err := cbor.Unmarshal(data, msg)
	if err != nil {
		return err
	}
	//update route table and process pending data if exists
	tp.routeTab.UpdateRoute(msg.src, lastHop, msg.orgTTL-msg.ttl)
	go tp.procPending(msg.src)
	switch msg.cmd {
	case cmdFind:
		return tp.procDataSend(lastHop, &msg)
	case cmdFindResp:
		return tp.procDataResp(&msg)
	}
	return fmt.Errorf("cmd not exist:%v", msg.cmd)

}

func (tp *TransP2P) procFindReq(src peer.ID, msg *MsgFindRoute) error {

	if strings.Compare(string(msg.dst), string(tp.self)) == 0 {
		//send response

		msg := MsgFindRoute{
			cmd:    cmdFindResp,
			dst:    msg.src,
			src:    tp.self,
			alpha:  alpha,
			orgTTL: msg.orgTTL - msg.ttl + 2,
			ttl:    msg.orgTTL - msg.ttl + 2,
		}
		data, err := cbor.Marshal(msg, cbor.EncOptions{})
		if err != nil {
			return err
		}
		tp.ppSvr.SendToPeer(src, handleRoute, data)
		return nil
	}
	//if ttl > 0 relay
	if msg.ttl > 0 {
		msg.ttl--
	} else {
		return fmt.Errorf("ttl out")
	}
	return tp.sendRouteDataToNearer(msg)

}
func (tp *TransP2P) procFindResp(msg *MsgFindRoute) error {
	if strings.Compare(string(msg.dst), string(tp.self)) == 0 {
		//send response
		//inform and send data
		return nil
	}
	//if ttl > 0 relay
	if msg.ttl > 0 {
		msg.ttl--
	} else {
		return fmt.Errorf("ttl out")
	}
	return tp.sendRouteDataToNearer(msg)
}
func (tp *TransP2P) procDataSend(src peer.ID, msg *MsgDataTrans) error {
	if strings.Compare(string(msg.dst), string(tp.self)) == 0 {
		//send response
		responsed := fmt.Sprintf("%v:%v", msg.src, msg.fd)
		_, ok := tp.responsedCache.Get(responsed)
		if ok {
			return nil
		}
		tp.responsedCache.Add(responsed, true)
		respMsg := &MsgDataTrans{cmd: cmdDataResp, dst: msg.src, src: tp.self, orgTTL: msg.orgTTL - msg.ttl + 2, ttl: msg.orgTTL - mst.ttl + 2, fd: msg.fd}
		if tp.handle != nil {
			go tp.handle(msg.data)
		}

		data, err := cbor.Marshal(respMsg, cbor.EncOptions{})
		if err != nil {
			return err
		}

		tp.ppSvr.SendToPeer(src, handleData, data)
	}
	//if ttl > 0 relay
	if msg.ttl > 0 {
		msg.ttl--
	} else {
		return fmt.Errorf("ttl out")
	}
	//TODO calculation data and token
	return tp.relayData(msg, false)
}
func (tp *TransP2P) procDataResp(msg *MsgDataTrans) error {
	if strings.Compare(string(msg.dst), string(tp.self)) == 0 {
		// finding who is wait for this
		result, ok := tp.waitingForResp.Load(msg.fd)
		if ok {
			if !reflect.ValueOf(result).IsNil() {
				val := result.(chan error)
				if val != nil {
					go func() {
						val <- nil
					}()
				}
			}

		}
	}
	//if ttl > 0 relay
	if msg.ttl > 0 {
		msg.ttl--
	} else {
		return fmt.Errorf("ttl out")
	}

	//TODO calculation data and token,  and resign a receipts to msg.receipts
	return tp.relayData(msg, false)
}
func (tp *TransP2P) addPending(msg *MsgDataTrans) {
	tp.lock.Lock()
	defer tp.lock.Unlock()
	pendings, ok := tp.pendingCache.Get(msg.dst)
	if ok {
		result := pendings.([]*MsgDataTrans)
		tp.pendingCache.Add(msg.dst, append(result, msg))
	} else {
		tp.pendingCache.Add(msg.dst, append([]*MsgDataTrans{}, msg))
	}
}

func (tp *TransP2P) procPending(dst peer.ID) {
	tp.lock.Lock()
	defer tp.lock.Unlock()
	pendings, ok := tp.pendingCache.Get(dst)
	if ok {
		result := pendings.([]*MsgDataTrans)
		for _, msg := range result {
			tp.relayData(msg, false)
		}
		tp.pendingCache.Remove(dst)
	}
}

/**
 *	relay data to next hop
 */
func (tp *TransP2P) relayData(msg *MsgDataTrans, autoFind bool) error {
	peers, err := tp.routeTab.GetRoutes(msg.dst)
	if len(peers) == 0 || err != nil {
		//store data and to find next?
		//now simple discard message
		if autoFind {

		}

	} else {

		data, err := cbor.Marshal(msg, cbor.EncOptions{})
		if err != nil {
			for _, nextPeer := range peers {
				tp.ppSvr.SendToPeer(nextPeer.next, handleRoute, data)
			}
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

//FindRoute Send to find a route command, ttl is the max-hop for find data and alpha is fan-out, outbound is optional and should be less than 64 bytes
// in the future, token should be provided
func (tp *TransP2P) FindRoute(targetPeer peer.ID, ttl uint8, alpha uint8, outbound []byte) error {

	if strings.Compare(string(targetPeer), string(tp.self)) == 0 {
		return nil
	}

	msg := MsgFindRoute{
		cmd:      cmdFind,
		dst:      targetPeer,
		src:      tp.self,
		alpha:    alpha,
		orgTTL:   ttl,
		ttl:      ttl,
		outbound: outbound,
	}
	return tp.sendRouteDataToNearer(&msg)
}

func (tp *TransP2P) sendRouteDataToNearer(msg *MsgFindRoute) error {

	//Get \alpha nearer peerid and send data to
	//this is with very low effiecny and only used for evaluation ,a tree should be  used to find nodes as fast as possible
	distsmap := make(map[int][]*peer.ID)
	for _, peerID := range tp.pstore.Peers() {
		dist := util.Dist(peerID, msg.dst)
		val, ok := distsmap[dist]
		if ok {
			distsmap[dist] = append(val, &peerID)
		} else {
			distsmap[dist] = append([]*peer.ID{}, &peerID)
		}
	}
	myDist := util.Dist(tp.self, msg.dst)

	target := make([]*peer.ID, 0)
	for i := 63; i > myDist; i-- {
		val, ok := distsmap[i]
		if ok {
			target := append(target, val...)

			if len(target) >= int(alpha) {
				break
			}
		}
	}

	if len(target) > int(alpha) {
		target = target[0:alpha]
	}

	data, err := cbor.Marshal(msg, cbor.EncOptions{})
	if err != nil {
		return err
	}
	if len(target) == 0 {
		return nil
	}
	tp.ppSvr.SendToPeers(target, handleRoute, data)
	return nil
}

//SendData Send to find a route command, ttl is the max-hop for find data and alpha is fan-out, outbound is optional and should be less than 64 bytes
// in the future, token should be provided
func (tp *TransP2P) SendData(targetPeer peer.ID, ttl uint8, alpha uint8, data []byte, ret chan error) error {

	if strings.Compare(string(targetPeer), string(tp.self)) == 0 {
		return nil
	}

	pending := PendingData{dst: targetPeer, alpha: alpha, orgTTL: ttl, data: data, wait: ret}
	go func() {
		tp.dataChannel <- &pending
	}()
	return nil
}

func (tp *TransP2P) createAndRelay(pending *PendingData) error {
	tp.currentFd++
	msg := &MsgDataTrans{
		cmd:    cmdSendData,
		dst:    pending.dst,
		src:    tp.self,
		orgTTL: pending.orgTTL,
		ttl:    pending.orgTTL,
		fd:     tp.currentFd,
		data:   pending.data,
		//receipts []byte //used for token consume,current is nil
	}
	if pending.wait != nil {
		tp.waitingForResp.Store(msg.fd, pending.wait)
	}
	return tp.relayData(msg, true)

}
