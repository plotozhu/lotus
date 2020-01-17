package transpp

import (
	"context"
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
	Hash      [CommHashLen]byte
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
type StreamProcessor func([]byte) error

//HandleStream 对流量进行的处理
func (hs *TransPushPullService) HandleStream(s network.Stream) {
	dec := cbor.NewDecoder(s)

	// decode into empty interface
	value := MsgPushPullData{}
	err := dec.Decode(&value)
	if err != nil {
		return
	}
	switch value.CommandID {
	case CmdPullHash:
		if hs.pendingData.Contains(value.Hash) {
			data, ok := hs.pendingData.Get(value.Hash)
			if ok {
				data2 := data.(*DataHandle)
				hs.sendStream(s, MsgPushPullData{CmdPushData, value.Hash, data2.Handle, data2.Data})
			}
		}
	case CmdPushHash:
		if !hs.receivedHashes.Contains(value.Hash) {
			hs.sendStream(s, MsgPushPullData{CmdPullHash, value.Hash, nil})
		}
	case CmdPushData:
		if !hs.receivedHashes.Contains(value.Hash) {
			hs.receivedHashes.Add(value.Hash, true)
			handles, _ := hs.procHandle.Load(value.Handle)
			for _, handleProc := range handles.([]StreamProcessor) {
				go func(v []byte) {
					handleProc(v)
				}(value.Data)
			}
		}
	}
}

//NewTransPushPullTransfer creating a push/pull object
func NewTransPushPullTransfer(h host.Host, pstore peerstore.Peerstore) *TransPushPullService {
	cache, _ := lru.New(CacheOfData)
	rcaches, _ := lru.New(CacheOfHashes)
	return &TransPushPullService{
		host:           h,
		pstore:         pstore,
		pendingData:    cache,
		receivedHashes: rcaches,
	}
}

//RegisterHandle is used to add a processor for handle, callback is invoked in go routine
func (hs *TransPushPullService) RegisterHandle(handle string, callback StreamProcessor) error {
	rawHandles, _ := hs.procHandle.Load(handle)
	handles := rawHandles.([]StreamProcessor)
	if handles == nil {
		handles = append([]StreamProcessor{}, callback)
	} else {
		exists := false
		for _, handle := range handles {
			if &handle == &callback {
				exists = true
				break
			}
		}
		if !exists {
			handles = append(handles, callback)
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
	handles := rawHandles.([]StreamProcessor)
	if handles == nil {
		return xerrors.Errorf("This handle is not registered")
	} else {
		exists := false
		for index, handle := range handles {
			if &handle == &callback {
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
	s.SetDeadline(time.Now().Add(10 * time.Second))
	defer s.SetDeadline(time.Time{})
	enc := cbor.NewEncoder(s, cbor.CanonicalEncOptions())

	err := enc.Encode(value)
	return err
}
func (hs *TransPushPullService) createSendStream(p peer.ID, value interface{}) error {
	s, err := hs.host.NewStream(network.WithNoDial(context.Background(), "should already have connection"), p, PPTransProtocolID)
	if err != nil {
		//	hs.pmgr.RemovePeer(p)
		return xerrors.Errorf("failed to open stream to peer: %w", err)
	}
	s.SetDeadline(time.Now().Add(10 * time.Second))
	defer s.SetDeadline(time.Time{})
	enc := cbor.NewEncoder(s, cbor.CanonicalEncOptions())

	err = enc.Encode(value)
	return err
}
func (hs *TransPushPullService) pushHash(p peer.ID, hash [CommHashLen]byte) error {
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
	if len(data) > 4*CommHashLen {
		hash := doHash(data)
		hashData := [CommHashLen]byte{}
		copy(hashData[:], hash[:CommHashLen])
		hs.pendingData.Add(hashData, &DataHandle{[]byte(handle), data})
		hs.pushHash(peerID, hashData)
	} else {
		hs.createSendStream(peerID, &DataHandle{[]byte(handle), data})
	}

}
func (hs *TransPushPullService) _SendToPeerAndWait(peerID peer.ID, data []byte) {

}

//SendToAllNeighbours send data to its all neighbours
func (hs *TransPushPullService) SendToAllNeighbours(handle string, data []byte) {
	for _, peerID := range hs.pstore.Peers() {
		hs.SendToPeer(peerID, handle, data)
	}
}
