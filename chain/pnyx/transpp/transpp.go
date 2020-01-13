package transpp

import (
	"context"
	"sync"
	"time"

	"github.com/filecoin-project/lotus/peermgr"
	"github.com/fxamacker/cbor"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/minio/blake2b-simd"
	"golang.org/x/xerrors"

	lru "github.com/hashicorp/golang-lru"
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

func doHash(b []byte) []byte {
	s := blake2b.Sum256(b)
	return s[:]
}

type TransPushPullService struct {
	host           host.Host
	pmgr           peermgr.MaybePeerMgr
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
				hs.sendStream(s, MsgPushPullData{CmdPushData, value.Hash, data.([]byte)})
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
func NewTransPushPullTransfer(h host.Host, pmgr peermgr.MaybePeerMgr) *TransPushPullService {
	cache, _ := lru.New(CacheOfData)
	rcaches, _ := lru.New(CacheOfHashes)
	return &TransPushPullService{
		host:           h,
		pmgr:           pmgr,
		pendingData:    cache,
		receivedHashes: rcaches,
	}
}
func (hs *TransPushPullService) RegisterHandle(handle string, callback StreamProcessor) error {
	raw_handles, _ := hs.procHandle.Load(handle)
	handles := raw_handles.([]StreamProcessor)
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
func (hs *TransPushPullService) UngisterHandle(handle string, callback StreamProcessor) error {
	raw_handles, _ := hs.procHandle.Load(handle)
	handles := raw_handles.([]StreamProcessor)
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
	return hs.createSendStream(p, MsgPushPullData{CmdPushHash, hash, nil})
}

func (hs *TransPushPullService) pushData(p peer.ID, hash [CommHashLen]byte, data []byte) error {
	return hs.createSendStream(p, MsgPushPullData{CmdPushData, hash, data})
}

func (hs *TransPushPullService) SendToPeers() {

}

//SendToPeer send data to peerId,
func (hs *TransPushPullService) SendToPeer(peerId peer.ID, data []byte) {
	hash := doHash(data)
	hashData := [CommHashLen]byte{}
	copy(hashData[:], hash[:CommHashLen])
	hs.pendingData.Add(hashData, data)
	hs.pushHash(peerId, hashData)
}
func (hs *TransPushPullService) SendToPeerAndWait(peerId peer.ID, data []byte) {

}
func (hs *TransPushPullService) SendToAllNeighbours() {

}
