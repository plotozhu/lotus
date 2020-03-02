package transpp

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"io"
	mrand "math/rand"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	crypto "github.com/libp2p/go-libp2p-crypto"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
	"github.com/multiformats/go-multiaddr"
	"github.com/smallnest/rpcx/log"
)

const port_base = 10000

func createnodes(nodesCount int) []multiaddr.Multiaddr {

	peers := make([]multiaddr.Multiaddr, 0)
	for i := 0; i < nodesCount; i++ {
		sourceMultiAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%v", port_base+i))
		if err != nil {
			fmt.Printf("error in creating id: %v\n", err)
		} else {
			peers = append(peers, sourceMultiAddr)
		}

	}
	return peers
}
func proc16(sender peer.ID, data []byte, info interface{}) error {
	chanRet := info.(chan bool)
	if data[0] == 0x16 {
		chanRet <- true
		return nil
	} else {
		chanRet <- false
		return fmt.Errorf("not 16")
	}
}
func ignore16(sender peer.ID, data []byte, info interface{}) error {

	if data[0] == 0x16 {
		log.Info("16 ignored")
		return nil
	} else {
		log.Info("error 16")
		return fmt.Errorf("not 16")
	}
}

func proc18(sender peer.ID, data []byte, info interface{}) error {
	chanRet := info.(chan bool)
	if data[0] == 0x18 {
		chanRet <- true
		return nil
	} else {
		chanRet <- false
		return fmt.Errorf("not 18")
	}
}

func ignore18(sender peer.ID, data []byte, info interface{}) error {

	if data[0] == 0x18 {
		log.Info("18 ignored")
		return nil
	} else {
		log.Info("error 18")
		return fmt.Errorf("not 18")
	}
}
func TestConnection(t *testing.T) {

	addrs_cnt := 20
	var nodes []host.Host

	addrs := createnodes(addrs_cnt)
	peerInfos := make([]*peerstore.PeerInfo, 0)

	// start a libp2p node that listens on TCP port 2000 on the IPv4
	// loopback interface
	for i := 0; i < addrs_cnt; i++ {
		r := mrand.New(mrand.NewSource(int64(i + port_base)))

		// Creates a new RSA key pair for this host.
		prvKey, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, r)
		if err != nil {
			panic(err)
		}
		node, err := libp2p.New(context.Background(),
			libp2p.ListenAddrStrings(addrs[i].String()),
			libp2p.Identity(prvKey),
		)
		if err != nil {
			panic(err)
		}

		// print the node's PeerInfo in multiaddr format
		peerInfo := &peerstore.PeerInfo{
			ID:    node.ID(),
			Addrs: node.Addrs(),
		}

		nodes = append(nodes, node)
		peerInfos = append(peerInfos, peerInfo)

	}

	srvs := make([]*TransPushPullService, 0)
	//每个节点连接后面的2个

	for i := 0; i < addrs_cnt; i++ {
		for j := i + 1; j < i+2; j++ {
			index := j % addrs_cnt
			if i == j {
				continue
			}
			// Extract the peer ID from the multiaddr.

			// Add the destination's peer multiaddress in the peerstore.
			// This will be used during connection and stream creation by libp2p.
			//nodes[i].Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)
			//peerID := nodes[index].ID()
			//	nodes[i].Peerstore().AddAddrs(peerID, nodes[index].Addrs(), peerstore.PermanentAddrTTL)
			//peerInfo := nodes[index].Peerstore().PeerInfo(nodes[index].ID())
			//	addrs, err := peerstore.InfoToP2pAddrs(peerInfos[index])
			err := nodes[i].Connect(context.Background(), peer.AddrInfo(*peerInfos[index]))
			if err != nil {
				panic(err)
			}
		}

		srvs = append(srvs, NewTransPushPullTransfer(context.TODO(), nodes[i]))
	}
	//选两个相邻的点测试一下
	//	for i := 0; i < addrs_cnt; i++ {
	chan16 := make(chan bool, 1)
	chan18 := make(chan bool, 1)
	srvs[16].RegisterHandle("/test1/short", proc16, chan16)
	srvs[18].RegisterHandle("/test2/long", proc18, chan18)

	srvs[18].RegisterHandle("/test1/short", ignore16, chan16)
	srvs[16].RegisterHandle("/test2/long", ignore18, chan18)
	//	}

	longData := make([]byte, 256)
	if _, err := io.ReadFull(crand.Reader, longData); err == nil {
		longData[0] = 0x18
		srvs[17].SendToAllNeighbours("/test2/long", longData)
	}
	srvs[17].SendToAllNeighbours("/test1/short", []byte{0x16})

	shortOk := <-chan16
	longOk := <-chan18
	if !shortOk {
		t.Fatal("short error")
	}
	if !longOk {
		t.Fatal("long error")
	}
	t.Log("test passed")
}
