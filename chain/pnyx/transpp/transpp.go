package transpp

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/fxamacker/cbor"
	lru "github.com/hashicorp/golang-lru"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	peer "github.com/libp2p/go-libp2p-peer"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
	"github.com/minio/blake2b-simd"
	"golang.org/x/xerrors"
)

const (
	//PPTransProtocolID is the protocolID for Push/Pull algorithm
	PPTransProtocolID = "/pnyx/ppt/0.1/"
)
const (
	//None
	_ uint8 = iota
	//CmdPushHash  of Push Hash 1
	CmdPushHash
	//CmdPullHash  2 client send pull request to retrieve data
	CmdPullHash
	//CmdPushData  3 server send data to client
	CmdPushData
)

const (
	//CommHashLen length of hash
	CommHashLen = 32
	//CacheOfData  max {hash,data} pairs to store
	CacheOfData = 1024
	//CacheOfHashes received blocks's hash
	CacheOfHashes = 16536
)

//MsgPushPullData is content format of push/pull
type MsgPushPullData struct {
	CommandID uint8
	Hash      []byte
	Handle    []byte
	Data      []byte
}

//DataHandle stored for retrieve
type DataHandle struct {
	Handle []byte
	Data   []byte
}

func doHash(b []byte) []byte {
	s := blake2b.Sum256(b)
	return s[:]
}

type rwInfo struct {
	rw *bufio.ReadWriter
	q  chan interface{}
}

//TransPushPullService basic interface
type TransPushPullService struct {
	host           host.Host
	pstore         peerstore.Peerstore
	pendingData    *lru.Cache
	receivedHashes *lru.Cache
	procHandle     sync.Map
	writeChan      map[network.Stream]*rwInfo
	wcmutex        sync.Mutex
}

// StreamProcessor is used for process actual byte stream
type StreamProcessor func(sender peer.ID, data []byte, info interface{}) error
type HandleItem struct {
	handle StreamProcessor
	pInfo  interface{}
}

//HandleStream 对流量进行的处理
func (hs *TransPushPullService) HandleStream(s network.Stream) {
	log.Println("Got a new stream!")

	// Create a buffer stream for non blocking read and write.
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))

	rwinfo := rwInfo{rw, make(chan interface{}, 10)}
	hs.wcmutex.Lock()
	hs.writeChan[s] = &rwinfo
	hs.wcmutex.Unlock()
	go hs.readDataRoutine(&rwinfo)
	go hs.writeDataRoutine(context.Background(), &rwinfo)
}

func (hs *TransPushPullService) readDataRoutine(rwinfo *rwInfo) {

	for {
		dec := cbor.NewDecoder(rwinfo.rw.Reader)

		value := MsgPushPullData{}
		err := dec.Decode(&value)
		if err != nil {
			fmt.Errorf("error in read data:%v", err)
			return
		}
		hashStr := string(value.Hash)
		switch value.CommandID {
		case CmdPullHash:
			if hs.pendingData.Contains(hashStr) {
				data, ok := hs.pendingData.Get(hashStr)
				if ok {
					data2 := data.(*DataHandle)

					if err == nil {
						hs.sendDataToRw(rwinfo, &MsgPushPullData{CmdPushData, value.Hash, data2.Handle, data2.Data})
						log.Println("on pull hash:", value.Hash)
					} else {
						fmt.Errorf("This handle is not registered")
					}

				} else {
					log.Printf("request hash does not exists %v.", hashStr)
				}
			}
		case CmdPushHash:
			if !hs.receivedHashes.Contains(hashStr) {
				if err == nil {
					hs.sendDataToRw(rwinfo, &MsgPushPullData{CmdPullHash, value.Hash, nil, nil})
					log.Println("on push hash:", value.Hash)
				} else {
					fmt.Errorf("This handle is not registered")
				}

			}
		case CmdPushData:
			if value.Hash == nil || !hs.receivedHashes.Contains(hashStr) {
				if value.Hash != nil {
					hs.receivedHashes.Add(hashStr, true)
				}
				log.Println("on push data:", value)
				handles, ok := hs.procHandle.Load(string(value.Handle))
				if ok {
					for _, handle := range handles.([]*HandleItem) {
						go func(thisPeer peer.ID, v []byte) {
							handle.handle(thisPeer, v, handle.pInfo)
						}(hs.host.ID(), value.Data)
					}
				}
			}
		}
	}
}

//写过程，接收队列的数据，然后把数据放进去
func (hs *TransPushPullService) writeDataRoutine(ctx context.Context, rwinfo *rwInfo) {
	go func(rw *rwInfo) {
		for {
			select {
			case data := <-rwinfo.q:
				enc, err := cbor.Marshal(data, cbor.CanonicalEncOptions())

				if err != nil {
					fmt.Println(fmt.Sprintf("error in encoding data:%v", err))
				} else {
					log.Println(">>Write data:", data)
					rw.rw.Write(enc)
					rw.rw.Flush()
				}
			case <-ctx.Done():
				return

			}
		}

	}(rwinfo)
}

func (hs *TransPushPullService) sendDataToRw(rwinfo *rwInfo, data interface{}) {

	go func() {
		rwinfo.q <- data
	}()

}
func (hs *TransPushPullService) sendDataToStream(s network.Stream, data interface{}) {
	hs.wcmutex.Lock()
	channel, ok := hs.writeChan[s]
	hs.wcmutex.Unlock()
	if ok {
		channel.q <- data
	} else {
		fmt.Errorf("error in sendStream: no rwInfo")
	}

}

//NewTransPushPullTransfer creating a push/pull object
func NewTransPushPullTransfer(ctx context.Context, h host.Host /*, pstore peerstore.Peerstore*/) *TransPushPullService {
	cache, _ := lru.New(CacheOfData)
	rcaches, _ := lru.New(CacheOfHashes)

	tps := &TransPushPullService{
		host:           h,
		pstore:         h.Peerstore(),
		pendingData:    cache,
		receivedHashes: rcaches,
		writeChan:      make(map[network.Stream]*rwInfo),
	}
	h.SetStreamHandler(PPTransProtocolID, tps.HandleStream)
	return tps

}

//RegisterHandle is used to add a processor for handle, callback is invoked in go routine
func (hs *TransPushPullService) RegisterHandle(handle string, callback StreamProcessor, info interface{}) error {
	rawHandles, ok := hs.procHandle.Load(handle)
	var handles []*HandleItem
	if !ok {
		handles = append([]*HandleItem{}, &HandleItem{callback, info})
	} else {
		handles := rawHandles.([]*HandleItem)
		exists := false
		for _, handle := range handles {
			if &handle.handle == &callback {
				exists = true
				break
			}
		}
		if !exists {
			handles = append(handles, &HandleItem{callback, info})
		} else {
			return xerrors.Errorf("This handle has been registered")
		}
	}
	hs.procHandle.Store(handle, handles)
	return nil
}

//UngisterHandle unregister a handle from push/pull system
func (hs *TransPushPullService) UngisterHandle(handle string, callback StreamProcessor) error {
	rawHandles, _ := hs.procHandle.Load(handle)
	handles := rawHandles.([]*HandleItem)
	if handles == nil {
		return xerrors.Errorf("This handle is not registered")
	} else {
		exists := false
		for index, handle := range handles {
			if &handle.handle == &callback {
				if index == 0 {
					handles = handles[1:]
				} else if index == len(handles)-1 {
					handles = handles[0:index]
				} else {
					handles = append(handles[0:index], handles[index+1:]...)
				}
				exists = true
				break
			}
		}
		if !exists {
			return xerrors.Errorf("This callback has not been registered")
		}
	}
	hs.procHandle.Store(handle, handles)
	return nil
}

//createStream，当节点之间还没有创建流的时候，可以使用此方式进行创建
func (hs *TransPushPullService) getRwInfo(p peer.ID) *rwInfo {
	//go func() {
	s, err := hs.host.NewStream(context.Background(), p, PPTransProtocolID)
	if err != nil {
		//	hs.pmgr.RemovePeer(p)
		fmt.Errorf("failed to open stream to peer: %w", err)
	}

	hs.wcmutex.Lock()
	rwinfo, ok := hs.writeChan[s]
	hs.wcmutex.Unlock()
	if !ok {
		// Turn the destination into a multiaddr.

		// Add the destination's peer multiaddress in the peerstore.
		// This will be used during connection and stream creation by libp2p.

		rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))
		newRwinfo := rwInfo{rw, make(chan interface{}, 10)}
		hs.writeChan[s] = &newRwinfo
		// Create a thread to read and write data.
		go hs.writeDataRoutine(context.Background(), &newRwinfo)
		go hs.readDataRoutine(&newRwinfo)
		return &newRwinfo
	} else {
		return rwinfo
	}

}

//SendToPeer send data to peerId, data with length < 4*CommHashLen will be send directly, otherwise it will be send by push/pull machenism
func (hs *TransPushPullService) SendToPeer(peerID peer.ID, handle string, data []byte) {
	var err error
	if len(data) > 4*CommHashLen {
		hash := doHash(data)
		hashData := make([]byte, CommHashLen)
		copy(hashData[:], hash[:CommHashLen])
		hs.pendingData.Add(string(hashData), &DataHandle{[]byte(handle), data})
		err = hs.pushHash(peerID, hashData)
	} else {
		rwinfo := hs.getRwInfo(peerID)
		if rwinfo != nil {
			hs.sendDataToRw(rwinfo, MsgPushPullData{CmdPushData, nil, []byte(handle), data})
		} else {
			log.Fatal("get rwinfo failed")
		}
	}

	if err != nil {
		fmt.Println(fmt.Sprintf("error in Sending to peer: %v", err))
	}

}
func (hs *TransPushPullService) _SendToPeerAndWait(peerID peer.ID, data []byte) {

}

//SendToAllNeighbours send data to its all neighbours
func (hs *TransPushPullService) SendToAllNeighbours(handle string, data []byte) {
	for _, peerID := range hs.pstore.Peers() {
		if strings.Compare(peerID.Pretty(), hs.host.ID().Pretty()) != 0 {
			hs.SendToPeer(peerID, handle, data)
		}

	}
}
func (hs *TransPushPullService) pushHash(p peer.ID, hash []byte) error {
	rwinfo := hs.getRwInfo(p)
	if rwinfo != nil {
		hs.sendDataToRw(rwinfo, MsgPushPullData{CmdPushHash, hash, nil, nil})
	} else {
		err := fmt.Errorf("push hash failed,no rwinfo")
		return err
	}
	return nil
}

//SendToPeers is used to send data to a series of peers
func (hs *TransPushPullService) SendToPeers(peers []*peer.ID, handle string, data []byte) {
	for _, peerID := range peers {
		hs.SendToPeer(*peerID, handle, data)
	}
}
