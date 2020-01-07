package storage

import (
	"bytes"
	"context"
	"io"
	"math"
	"math/rand"
	"time"

	"golang.org/x/xerrors"

	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/chain/actors"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/lib/sectorbuilder"
)

/**
	pledgeSector实际上执行的是用一个随机数据填满一个扇区
	//QZ TODO 为什么一定要用随机数填充？用全0不行么？
 */
func (m *Miner) pledgeSector(ctx context.Context, sectorID uint64, existingPieceSizes []uint64, sizes ...uint64) ([]Piece, error) {
	if len(sizes) == 0 {
		return nil, nil
	}

	start := time.Now()
	deals := make([]actors.StorageDealProposal, len(sizes))
	//对现存所有的pieces产生commitent，然后进行签名
	//目标是在链上请求存储此数据
	for i, size := range sizes { //测试中肯定是只有一个
		release := m.sb.RateLimit()
		commP, err := sectorbuilder.GeneratePieceCommitment(io.LimitReader(rand.New(rand.NewSource(42)), int64(size)), size)
		release()

		if err != nil {
			return nil, err
		}

		sdp := actors.StorageDealProposal{
			PieceRef:             commP[:],
			PieceSize:            size,
			//交易的双方
			Client:               m.worker,
			Provider:             m.maddr,
			//本提议的超时时间
			ProposalExpiration:   math.MaxUint64,
			//数据的存储时间
			Duration:             math.MaxUint64 / 2, // /2 because overflows
			//每个 Epoch的存储价格
			StoragePricePerEpoch: types.NewInt(0),
			//抵押物， 应该是 worker提供的，
			StorageCollateral:    types.NewInt(0),
			//这个是最后的签名
			ProposerSignature:    nil,
		}

		//对sdp数据进行签名
		if err := api.SignWith(ctx, m.api.WalletSign, m.worker, &sdp); err != nil {
			return nil, xerrors.Errorf("signing storage deal failed: ", err)
		}

		deals[i] = sdp
	}

	log.Warn(xerrors.Errorf("[qz1]: time to create :%v", time.Since(start).Milliseconds()))
	start = time.Now()
	params, aerr := actors.SerializeParams(&actors.PublishStorageDealsParams{
		Deals: deals,
	})
	if aerr != nil {
		return nil, xerrors.Errorf("serializing PublishStorageDeals params failed: ", aerr)
	}

	smsg, err := m.api.MpoolPushMessage(ctx, &types.Message{
		To:       actors.StorageMarketAddress,
		From:     m.worker,
		Value:    types.NewInt(0),
		GasPrice: types.NewInt(0),
		GasLimit: types.NewInt(1000000),
		Method:   actors.SMAMethods.PublishStorageDeals,
		Params:   params,
	})
	///// ------------------这一段是将存储信息的结果提交到链上    --------------
	if err != nil {
		return nil, err
	}
	//等待链上提交完成，这个是看到自身提交的的cid在链上出
	r, err := m.api.StateWaitMsg(ctx, smsg.Cid())
	if err != nil {
		return nil, err
	}
	if r.Receipt.ExitCode != 0 {
		log.Error(xerrors.Errorf("publishing deal failed: exit %d", r.Receipt.ExitCode))
	}
	log.Warn(xerrors.Errorf("[qz1]: time to waiting for deal ok :%v", time.Since(start).Milliseconds()))
	start = time.Now()
	var resp actors.PublishStorageDealResponse
	if err := resp.UnmarshalCBOR(bytes.NewReader(r.Receipt.Return)); err != nil {
		return nil, err
	}
	if len(resp.DealIDs) != len(sizes) {
		return nil, xerrors.New("got unexpected number of DealIDs from PublishStorageDeals")
	}

	out := make([]Piece, len(sizes))

	/**
		根据链上确认的结果，首先将piece的信息存入到sector里
		//Qz TODO 此处的数据是io.LimitReader(rand.New(rand.NewSource(42))随机生成的，而out中又有原来数据中的DealIDs和新数据的commP
	   // 此处意味着DealId和commP根本不需要对应！！！
	 */
	for i, size := range sizes {
		ppi, err := m.sb.AddPiece(size, sectorID, io.LimitReader(rand.New(rand.NewSource(42)), int64(size)), existingPieceSizes)
		if err != nil {
			return nil, err
		}

		existingPieceSizes = append(existingPieceSizes, size)

		out[i] = Piece{
			DealID: resp.DealIDs[i],
			Size:   ppi.Size,
			CommP:  ppi.CommP[:],
		}
	}
	log.Warn(xerrors.Errorf("[qz1]: time to fill piece :%v", time.Since(start).Milliseconds()))
	//	start =time.Now()
	return out, nil
}

/**
模拟生成扇区
	获取扇区大小
	获取一个空的扇区ID
	通过pledgeSector填充扇区数据
*/
func (m *Miner) PledgeSector() error {
	go func() {
		ctx := context.TODO() // we can't use the context from command which invokes
		// this, as we run everything here async, and it's cancelled when the
		// command exits

		size := sectorbuilder.UserBytesForSectorSize(m.sb.SectorSize())

		sid, err := m.sb.AcquireSectorId()
		if err != nil {
			log.Errorf("%+v", err)
			return
		}

		pieces, err := m.pledgeSector(ctx, sid, []uint64{}, size)
		if err != nil {
			log.Errorf("%+v", err)
			return
		}

		if err := m.newSector(context.TODO(), sid, pieces[0].DealID, pieces[0].ppi()); err != nil {
			log.Errorf("%+v", err)
			return
		}
	}()
	return nil
}
