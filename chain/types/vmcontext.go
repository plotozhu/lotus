package types

import (
	"context"

	"github.com/filecoin-project/go-address"

	"github.com/filecoin-project/go-amt-ipld"
	"github.com/filecoin-project/lotus/chain/actors/aerrors"
	cid "github.com/ipfs/go-cid"
	hamt "github.com/ipfs/go-hamt-ipld"
	cbg "github.com/whyrusleeping/cbor-gen"
)

/**
存储系统

*/
type Storage interface {
	Put(cbg.CBORMarshaler) (cid.Cid, aerrors.ActorError)
	Get(cid.Cid, cbg.CBORUnmarshaler) aerrors.ActorError

	GetHead() cid.Cid

	// Commit sets the new head of the actors state as long as the current
	// state matches 'oldh'
	Commit(oldh cid.Cid, newh cid.Cid) aerrors.ActorError
}

/**
状态树
*/
type StateTree interface {
	SetActor(addr address.Address, act *Actor) error
	GetActor(addr address.Address) (*Actor, error)
}

/***
这是最基础的虚拟机的上下文
*/
type VMContext interface {
	Message() *Message                                                                                //本次执行的消息
	Origin() address.Address                                                                          //执行者
	Ipld() *hamt.CborIpldStore                                                                        //存储的数据根
	Send(to address.Address, method uint64, value BigInt, params []byte) ([]byte, aerrors.ActorError) //执行发送指令
	BlockHeight() uint64                                                                              //区块的高度
	GasUsed() BigInt                                                                                  //执行中使用的GAS
	Storage() Storage                                                                                 //记录的存储信息
	StateTree() (StateTree, aerrors.ActorError)                                                       //感觉是读取状态树根
	VerifySignature(sig *Signature, from address.Address, data []byte) aerrors.ActorError             //签名验证
	ChargeGas(uint64) aerrors.ActorError                                                              //添加使用的GAS
	GetRandomness(height uint64) ([]byte, aerrors.ActorError)                                         //获取随机数
	GetBalance(address.Address) (BigInt, aerrors.ActorError)                                          //获得余额
	Sys() *VMSyscalls

	Context() context.Context
}

type VMSyscalls struct {
	ValidatePoRep func(context.Context, address.Address, uint64, []byte, []byte, []byte, []byte, []byte, uint64) (bool, aerrors.ActorError)
}

type storageWrapper struct {
	s Storage
}

func (sw *storageWrapper) Put(i cbg.CBORMarshaler) (cid.Cid, error) {
	c, err := sw.s.Put(i)
	if err != nil {
		return cid.Undef, err
	}

	return c, nil
}

func (sw *storageWrapper) Get(c cid.Cid, out cbg.CBORUnmarshaler) error {
	if err := sw.s.Get(c, out); err != nil {
		return err
	}

	return nil
}

func WrapStorage(s Storage) amt.Blocks {
	return &storageWrapper{s}
}
