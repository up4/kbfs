// Copyright 2016 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

package libkbfs

import (
	"fmt"

	"golang.org/x/net/context"
)

// BlockOpsStandard implements the BlockOps interface by relaying
// requests to the block server.
type BlockOpsStandard struct {
	config Config
}

var _ BlockOps = (*BlockOpsStandard)(nil)

// Get implements the BlockOps interface for BlockOpsStandard.
func (b *BlockOpsStandard) Get(ctx context.Context, md *RootMetadata,
	blockPtr BlockPointer, block Block) error {
	bserv := b.config.BlockServer()
	buf, blockServerHalf, err := bserv.Get(ctx, blockPtr.ID, md.ID, blockPtr.BlockContext)
	if err != nil {
		// Temporary code to track down bad block
		// requests. Remove when not needed anymore.
		if _, ok := err.(BServerErrorBadRequest); ok {
			panic(fmt.Sprintf("Bad BServer request detected: err=%s, blockPtr=%s",
				err, blockPtr))
		}

		return err
	}

	crypto := b.config.Crypto()
	if err := crypto.VerifyBlockID(buf, blockPtr.ID); err != nil {
		return err
	}

	tlfCryptKey, err := b.config.KeyManager().
		GetTLFCryptKeyForBlockDecryption(ctx, md, blockPtr)
	if err != nil {
		return err
	}

	// construct the block crypt key
	blockCryptKey, err := crypto.UnmaskBlockCryptKey(
		blockServerHalf, tlfCryptKey)
	if err != nil {
		return err
	}

	var encryptedBlock EncryptedBlock
	err = b.config.Codec().Decode(buf, &encryptedBlock)
	if err != nil {
		return err
	}

	// decrypt the block
	err = crypto.DecryptBlock(encryptedBlock, blockCryptKey, block)
	if err != nil {
		return err
	}

	block.SetEncodedSize(uint32(len(buf)))
	return nil
}

// Ready implements the BlockOps interface for BlockOpsStandard.
func (b *BlockOpsStandard) Ready(ctx context.Context, md *RootMetadata,
	block Block) (id BlockID, plainSize int, readyBlockData ReadyBlockData,
	err error) {
	defer func() {
		if err != nil {
			id = BlockID{}
			plainSize = 0
			readyBlockData = ReadyBlockData{}
		}
	}()

	crypto := b.config.Crypto()

	tlfCryptKey, err := b.config.KeyManager().
		GetTLFCryptKeyForEncryption(ctx, md)
	if err != nil {
		return
	}

	// New server key half for the block.
	serverHalf, err := crypto.MakeRandomBlockCryptKeyServerHalf()
	if err != nil {
		return
	}

	blockKey, err := crypto.UnmaskBlockCryptKey(serverHalf, tlfCryptKey)
	if err != nil {
		return
	}

	plainSize, encryptedBlock, err := crypto.EncryptBlock(block, blockKey)
	if err != nil {
		return
	}

	buf, err := b.config.Codec().Encode(encryptedBlock)
	if err != nil {
		return
	}

	readyBlockData = ReadyBlockData{
		buf:        buf,
		serverHalf: serverHalf,
	}

	encodedSize := readyBlockData.GetEncodedSize()
	if encodedSize < plainSize {
		err = TooLowByteCountError{
			ExpectedMinByteCount: plainSize,
			ByteCount:            encodedSize,
		}
		return
	}

	id, err = crypto.MakePermanentBlockID(buf)
	if err != nil {
		return
	}

	// Cache the encoded size.
	block.SetEncodedSize(uint32(encodedSize))

	return
}

// Put implements the BlockOps interface for BlockOpsStandard.
func (b *BlockOpsStandard) Put(ctx context.Context, md *RootMetadata,
	blockPtr BlockPointer, readyBlockData ReadyBlockData) error {
	bserv := b.config.BlockServer()
	if blockPtr.RefNonce == zeroBlockRefNonce {
		return bserv.Put(ctx, blockPtr.ID, md.ID, blockPtr.BlockContext, readyBlockData.buf,
			readyBlockData.serverHalf)
	}
	// non-zero block refnonce means this is a new reference to an
	// existing block.
	return bserv.AddBlockReference(ctx, blockPtr.ID, md.ID, blockPtr.BlockContext)
}

// Delete implements the BlockOps interface for BlockOpsStandard.
func (b *BlockOpsStandard) Delete(ctx context.Context, md *RootMetadata,
	ptrs []BlockPointer) (liveCounts map[BlockID]int, err error) {
	contexts := make(map[BlockID][]BlockContext)
	for _, ptr := range ptrs {
		contexts[ptr.ID] = append(contexts[ptr.ID], ptr.BlockContext)
	}
	return b.config.BlockServer().RemoveBlockReference(ctx, md.ID, contexts)
}

// Archive implements the BlockOps interface for BlockOpsStandard.
func (b *BlockOpsStandard) Archive(ctx context.Context, md *RootMetadata,
	ptrs []BlockPointer) error {
	contexts := make(map[BlockID][]BlockContext)
	for _, ptr := range ptrs {
		contexts[ptr.ID] = append(contexts[ptr.ID], ptr.BlockContext)
	}

	return b.config.BlockServer().ArchiveBlockReferences(ctx, md.ID, contexts)
}
