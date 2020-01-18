package transp2p

import (
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
	maxPendDataCnt = 1024
	handleRoute    = "ROUTE/CMD"
	handleData     = "ROUTE/DATA"
	cmdFind        = 0x01
	cmdFindResp    = 0x02
	cmdSendData    = 0x10
	cmdDataResp    = 0x11
)

type TransP2P struct {
	ppSvr    *transpp.TransPushPullService
	pendData *lru.Cache
	self     peer.ID
	pstore   peerstore.Peerstore
	buckets  sync.Map
}
type MsgFindRoute struct {
	cmd      uint8
	dst      peer.ID
	src      peer.ID
	alpha    uint8
	orgTTL   uint8
	ttl      uint8
	outbound []byte
}
type MsgDataTrans struct {
	cmd    uint8
	dst    peer.ID
	src    peer.ID
	alpha  uint8
	orgTTL uint8
	ttl    uint8
	fd     uint64
	data   []byte
}

func CreateTransP2P(ppSvr *transpp.TransPushPullService, self peer.ID, pstore peerstore.Peerstore) *TransP2P {

	transInst := TransP2P{
		ppSvr:    ppSvr,
		pendData: lru.New(maxPendDataCnt),
		self:     self,
		pstore:   pstore,
	}
	ppSvr.RegisterHandle(handleRoute, transInst.procRouteInfo)
	ppSvr.RegisterHandle(handleData, transInst.procDataArrival)
	return &transInst
}

func (tp *TransP2P) procRouteInfo(data []byte) error {
	var msg MsgFindRoute
	err := cbor.Unmarshal(data, msg)
	if err != nil {
		return err
	}
	switch msg.cmd {
	case cmdFind:
		tp.procFindReq(msg)
	case cmdFindResp:

	}

}

func (tp *TransP2P) procDataArrival(data []byte) error {

}

func (tp *TransP2P) procFindReq(msg *MsgFindRoute) error {

}
func (tp *TransP2P) procFindResp(data []byte) error {

}
func (tp *TransP2P) procDataSend(data []byte) error {

}
func (tp *TransP2P) procDataResp(data []byte) error {

}

func (tp *TransP2P) AddPeer(peerID peer.ID) {

}
func (tp *TransP2P) RemovePeer(peer.ID, peer.ID) {

}

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
		dst:      targetPeer,
		src:      tp.self,
		alpha:    alpha,
		orgTTL:   ttl,
		ttl:      ttl,
		outbound: outbound,
	}

	//Get \alpha nearer peerid and send data to
	//this is with very low effiecny and only used for evaluation ,a tree should be  used to find nodes as fast as possible
	distsmap := make(map[int][]*peer.ID)
	for _, peerID := range tp.pstore.Peers() {
		dist := util.Dist(peerID, targetPeer)
		val, ok := distsmap[dist]
		if ok {
			distsmap[dist] = append(val, &peerID)
		} else {
			distsmap[dist] = append([]*peer.ID{}, &peerID)
		}
	}
	myDist := util.Dist(tp.self, targetPeer)

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
