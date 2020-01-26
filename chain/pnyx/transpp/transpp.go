package transpp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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
	PPTransProtocolID = "/pnyx/pptrans/0.1/"
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

//TransPushPullService basic interface
type TransPushPullService struct {
	host           host.Host
	pstore         peerstore.Peerstore
	pendingData    *lru.Cache
	receivedHashes *lru.Cache
	procHandle     sync.Map
}

// StreamProcessor is used for process actual byte stream
type StreamProcessor func(sender peer.ID, data []byte, info interface{}) error
type HandleItem struct {
	handle StreamProcessor
	pInfo  interface{}
}

//HandleStream 对流量进行的处理
func (hs *TransPushPullService) HandleStream(s network.Stream) {
	dec := cbor.NewDecoder(s)

	// decode into empty interface
	value := MsgPushPullData{}
	err := dec.Decode(&value)
	if err != nil {
		return
	}
	hashStr := string(value.Hash)
	switch value.CommandID {
	case CmdPullHash:
		if hs.pendingData.Contains(hashStr) {
			data, ok := hs.pendingData.Get(hashStr)
			if ok {
				data2 := data.(*DataHandle)
				retStream, err := s.Conn().NewStream()
				if err == nil {
					hs.sendStream(retStream, MsgPushPullData{CmdPushData, value.Hash, data2.Handle, data2.Data})
				} else {
					fmt.Errorf("This handle is not registered")
				}

			}
		}
	case CmdPushHash:
		if !hs.receivedHashes.Contains(hashStr) {
			retStream, err := s.Conn().NewStream()
			if err == nil {
				hs.sendStream(retStream, &MsgPushPullData{CmdPullHash, value.Hash, nil, nil})
			} else {
				fmt.Errorf("This handle is not registered")
			}

		}
	case CmdPushData:
		if value.Hash == nil || !hs.receivedHashes.Contains(value.Hash) {
			if value.Hash != nil {
				hs.receivedHashes.Add(value.Hash, true)
			}

			handles, _ := hs.procHandle.Load(string(value.Handle))
			for _, handle := range handles.([]*HandleItem) {
				go func(thisPeer peer.ID, v []byte) {
					handle.handle(thisPeer, v, handle.pInfo)
				}(hs.host.ID(), value.Data)
			}
		}
	}
}

//NewTransPushPullTransfer creating a push/pull object
func NewTransPushPullTransfer(h host.Host /*, pstore peerstore.Peerstore*/) *TransPushPullService {
	cache, _ := lru.New(CacheOfData)
	rcaches, _ := lru.New(CacheOfHashes)

	tps := &TransPushPullService{
		host:           h,
		pstore:         h.Peerstore(),
		pendingData:    cache,
		receivedHashes: rcaches,
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

func (hs *TransPushPullService) sendStream(s network.Stream, value interface{}) error {
	defer s.SetDeadline(time.Now().Add(10 * time.Second))
	//	defer s.SetDeadline(time.Time{})
	enc, err := cbor.Marshal(value, cbor.CanonicalEncOptions())

	//	err := enc.Encode(value)
	s.Write(enc)
	return err
}
func (hs *TransPushPullService) createSendStream(p peer.ID, value interface{}) error {
	s, err := hs.host.NewStream(context.Background(), p, PPTransProtocolID)
	if err != nil {
		//	hs.pmgr.RemovePeer(p)
		return xerrors.Errorf("failed to open stream to peer: %w", err)
	}

	return hs.sendStream(s, value)
}
func (hs *TransPushPullService) pushHash(p peer.ID, hash []byte) error {
	return hs.createSendStream(p, MsgPushPullData{CmdPushHash, hash, nil, nil})
}

//SendToPeers is used to send data to a series of peers
func (hs *TransPushPullService) SendToPeers(peers []*peer.ID, handle string, data []byte) {
	for _, peerID := range peers {
		hs.SendToPeer(*peerID, handle, data)
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
		err = hs.createSendStream(peerID, MsgPushPullData{CmdPushData, nil, []byte(handle), data})
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
