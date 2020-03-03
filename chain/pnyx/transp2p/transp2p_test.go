package transp2p

import (
	"context"
	"fmt"
	"math/rand"
	mrand "math/rand"
	"sort"
	"testing"
	"time"

	"github.com/bluele/gcache"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	crypto "github.com/libp2p/go-libp2p-crypto"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
	"github.com/multiformats/go-multiaddr"
)

/***
 * 测试目标：
 * 1. 路由测试
 *    创建、删除路由
 * 2. 收发测试
 *    发现路由、删除路由
 *    发送数据
 * 		成功接收发送数据
 *      发送失败
 * 		路由切换
 * 		1 删除一个中间设备，发送成功
 *      2 删除所有的中间设备，发送
 */
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

type SortableHosts []host.Host

func (a SortableHosts) Len() int           { return len(a) }
func (a SortableHosts) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a SortableHosts) Less(i, j int) bool { return string(a[i].ID()) < string(a[j].ID()) }

type SortablePeerStore []*peerstore.PeerInfo

func (a SortablePeerStore) Len() int           { return len(a) }
func (a SortablePeerStore) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a SortablePeerStore) Less(i, j int) bool { return string(a[i].ID) < string(a[j].ID) }

func procData(data []byte) {
	fmt.Println(data)
}
func TestConnection(t *testing.T) {

	addrsCnt := 20
	var nodes SortableHosts

	addrs := createnodes(addrsCnt)
	peerInfos := make(SortablePeerStore, 0)

	// start a libp2p node that listens on TCP port 2000 on the IPv4
	// loopback interface
	for i := 0; i < addrsCnt; i++ {

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

	}
	//重新排列下，后面处理方便些

	sort.Sort(peerInfos)
	sort.Sort(nodes)
	for i, peerInfo := range peerInfos {
		fmt.Printf("%v：%v\n", i, peer.IDHexEncode(peerInfo.ID))
	}
	srvs := make([]*TransP2P, 0)
	obs := make([]*MyObserver, 0)
	//连接，从1到addrCnt，每个都连接后面的（7/14/21/28）mod addrCnt	节点
	for i := 0; i < addrsCnt; i++ {
		fmt.Printf("\n %v -> [", i)
		connected := make(map[int]bool)
	breakout:
		for {
			rand.Seed(time.Now().UnixNano())
			index := (i + rand.Intn(1000)) % addrsCnt
			if i == index {
				continue
			}
			if connected[index] {
				continue
			}
			connected[index] = true
			fmt.Printf("%v,", index)
			err := nodes[i].Connect(context.Background(), peer.AddrInfo(*peerInfos[index]))
			if err != nil {
				panic(err)
			}
			if len(connected) >= 4 {
				break breakout
			}

		}
		fmt.Print(" ]")
		ppSrv, _ := NewTransP2P(context.TODO(), nodes[i], nil, procData)
		observer := NewObserver(fmt.Sprintf("OBS%v", i))
		ppSrv.Attach(observer)
		obs = append(obs, observer)
		srvs = append(srvs, ppSrv)

	}
	srvs[0].FindRoute(peerInfos[19].ID, 5, 2, nil)

	<-time.NewTimer(10 * time.Second).C
	for _, observer := range obs {
		fmt.Printf("------------- routetab of %v------------\n", observer.GetID())
		fmt.Println(observer.Print())
	}
	ret := make(chan error)
	srvs[1].SendData(peerInfos[18].ID, 5, 2, ([]byte{0x01, 0x02, 0x03, 0x04, 0x05})[:], ret)
	result := <-ret
	if result == nil {
		t.Log("send OK")
	} else {
		t.Fatalf("send failed %v", result)
	}

}

//P2PObserver observer interface for TransP2P
type MyObserver struct {
	id     string
	routes gcache.Cache
}

func NewObserver(str string) *MyObserver {
	return &MyObserver{
		id:     str,
		routes: gcache.New(1000).LRU().Build(),
	}
}
func (mo *MyObserver) PeerConnect(peerID peer.ID) {

}
func (mo *MyObserver) PeerDisconnect(peerID peer.ID) {

}
func (mo *MyObserver) RouteItemUpdated(dst peer.ID, routeItems []*RouteTableItem) {
	fmt.Printf("route updated:%v", dst)
	mo.routes.Set(dst, routeItems)
}

//GetID is used to identify observer, same id means same observer
func (mo *MyObserver) GetID() string {
	return mo.id
}

//GetID is used to identify observer, same id means same observer
func (mo *MyObserver) Print() string {
	result := ""
	for dst, items := range mo.routes.GetALL(false) {
		result += fmt.Sprintf("dst:%v \t {", dst.(peer.ID))
		for _, item := range items.([]*RouteTableItem) {
			result += fmt.Sprintf("\t%v:%v,", item.next, item.ttl)
		}
		result += "}\n"
	}
	return result
}
