package transp2p

import (
	"context"
	"fmt"
	mrand "math/rand"
	"testing"
	"time"

	rtutil "github.com/filecoin-project/lotus/chain/pnyx/rtutil"
	"github.com/filecoin-project/lotus/chain/pnyx/transpp"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	crypto "github.com/libp2p/go-libp2p-crypto"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
	"github.com/multiformats/go-multiaddr"
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
func proc17(sender peer.ID, data []byte, info interface{}) error {
	if data[0] == 0x17 {
		return nil
	} else {
		return fmt.Errorf("not 17")
	}
}

func proc18(sender peer.ID, data []byte, info interface{}) error {
	if data[0] == 0x18 {
		return nil
	} else {
		return fmt.Errorf("not 18")
	}
}
func TestConnection(t *testing.T) {

	addrs_cnt := 100
	var nodes []host.Host

	addrs := createnodes(addrs_cnt)
	peerInfos := make([]*peerstore.PeerInfo, 0)

	// start a libp2p node that listens on TCP port 2000 on the IPv4
	// loopback interface
	for i := 0; i < addrs_cnt; i++ {

		r := mrand.New(mrand.NewSource(int64(i+port_base) + time.Now().UnixNano()))

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

		fmt.Printf("%v：%v\n", i, peer.IDHexEncode(node.ID()))
	}
	for i := 0; i < addrs_cnt; i++ {
		fmt.Println("")
		for j := 0; j < addrs_cnt; j++ {
			fmt.Printf("\t%v", rtutil.Dist(peerInfos[i].ID, peerInfos[j].ID))
		}

	}

	srvs := make([]*transpp.TransPushPullService, 0)
	//每个节点连接后面的4个

	for i := 0; i < addrs_cnt; i++ {
		for j := i + 1; j < i+4; j++ {
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

		srvs = append(srvs, transpp.NewTransPushPullTransfer(context.TODO(), nodes[i]))
	}

}
