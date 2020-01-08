package actors

import (
	"github.com/filecoin-project/go-address"
	cid "github.com/ipfs/go-cid"
)

/**
 * 这是代理人的处理过程
 */

const (
	
)

type VoteReq struct {
	parentID cid.Cid
	height   uint64
	sender   address.Address
}
type DelegateActor struct {
	committee []address.Address //记录的所有委员会成员信息

	candidates []VoteReq
}


func CreateDelegate(address.Address)(*DelegateActor, error){

	return nil,error.new("type")
}
func (da * DeletageActor)