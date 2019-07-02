// Copyright 2019, Keychain Foundation Ltd.
// This file is part of the dipperin-core library.
//
// The dipperin-core library is free software: you can redistribute
// it and/or modify it under the terms of the GNU Lesser General Public License
// as published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// The dipperin-core library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package state_processor

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/dipperin/dipperin-core/common"
	"github.com/dipperin/dipperin-core/common/util"
	"github.com/dipperin/dipperin-core/core/model"
	"github.com/dipperin/dipperin-core/core/vm"
	"github.com/dipperin/dipperin-core/core/vm/common/utils"
	model2 "github.com/dipperin/dipperin-core/core/vm/model"
	"github.com/dipperin/dipperin-core/tests/g-testData"
	"github.com/dipperin/dipperin-core/third-party/crypto"
	"github.com/dipperin/dipperin-core/third-party/crypto/cs-crypto"
	"github.com/dipperin/dipperin-core/third-party/log"
	"github.com/dipperin/dipperin-core/third-party/trie"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/stretchr/testify/assert"
	"io/ioutil"
	"math/big"
	"strings"
	"testing"
	"time"
)

var (
	testPriv1 = "289c2857d4598e37fb9647507e47a309d6133539bf21a8b9cb6df88fd5232031"
	testPriv2 = "289c2857d4598e37fb9647507e47a309d6133539bf21a8b9cb6df88fd5232032"

	aliceAddr   = common.HexToAddress("0x00005586B883Ec6dd4f8c26063E18eb4Bd228e59c3E9")
	bobAddr     = common.HexToAddress("0x0000970e8128aB834E8EAC17aB8E3812f010678CF791")
	charlieAddr = common.HexToAddress("0x00007dbbf084F4a6CcC070568f7674d4c2CE8CD2709E")

	TrieError = errors.New("trie error")

	testGasPrice = big.NewInt(2)
	testGasLimit = uint64(2100000)
)

func createKey() (*ecdsa.PrivateKey, *ecdsa.PrivateKey) {
	key1, err1 := crypto.HexToECDSA(testPriv1)
	key2, err2 := crypto.HexToECDSA(testPriv2)
	if err1 != nil || err2 != nil {
		return nil, nil
	}
	return key1, key2
}

func createTestTx() (*model.Transaction, *model.Transaction) {
	key1, key2 := createKey()
	fs1 := model.NewMercurySigner(big.NewInt(1))
	fs2 := model.NewMercurySigner(big.NewInt(3))
	testTx1 := model.NewTransaction(0, bobAddr, big.NewInt(200), g_testData.TestGasPrice, g_testData.TestGasLimit, []byte{})
	testTx1.SignTx(key1, fs1)
	testTx2 := model.NewTransaction(0, aliceAddr, big.NewInt(10), g_testData.TestGasPrice, g_testData.TestGasLimit, []byte{})
	testTx2.SignTx(key2, fs2)
	return testTx1, testTx2
}

func createContractTx(t *testing.T, code, abi string) *model.Transaction {
	key, _ := createKey()
	fs := model.NewMercurySigner(big.NewInt(1))
	data := getContractCode(t, code, abi)
	to := common.HexToAddress(common.AddressContractCreate)
	tx := model.NewTransactionSc(0, &to, big.NewInt(200), testGasPrice, testGasLimit, data)
	tx.SignTx(key, fs)
	tx.PaddingTxIndex(0)
	return tx
}

func callContractTx(t *testing.T, to *common.Address, funcName string, param [][]byte, nonce uint64) *model.Transaction {
	key, _ := createKey()
	fs := model.NewMercurySigner(big.NewInt(1))
	data := getContractInput(t, funcName, param)
	tx := model.NewTransactionSc(nonce, to, big.NewInt(200), testGasPrice, testGasLimit, data)
	tx.SignTx(key, fs)
	tx.PaddingTxIndex(0)
	return tx
}

func CreateBlock(num uint64, preHash common.Hash, txList []*model.Transaction, limit uint64) *model.Block {
	header := model.NewHeader(1, num, preHash, common.HexToHash("123456"), common.HexToDiff("1fffffff"), big.NewInt(time.Now().UnixNano()), bobAddr, common.BlockNonce{})

	// vote
	var voteList []model.AbstractVerification
	header.GasLimit = limit
	block := model.NewBlock(header, txList, voteList)

	// calculate block nonce
	model.CalNonce(block)
	block.RefreshHashCache()
	return block
}

func CreateTestStateDB() (ethdb.Database, common.Hash) {
	db := ethdb.NewMemDatabase()

	//todo The new method does not take the tree from the underlying database
	tdb := NewStateStorageWithCache(db)
	processor, _ := NewAccountStateDB(common.Hash{}, tdb)
	processor.NewAccountState(aliceAddr)
	processor.NewAccountState(bobAddr)
	processor.AddBalance(aliceAddr, big.NewInt(9e6))

	root, _ := processor.Commit()
	tdb.TrieDB().Commit(root, false)
	return db, root
}

func createSignedVote(num uint64, blockId common.Hash, voteType model.VoteMsgType, testPriv string, address common.Address) *model.VoteMsg {
	voteA := model.NewVoteMsg(num, num, blockId, voteType)
	hash := common.RlpHashKeccak256(voteA)
	key, _ := crypto.HexToECDSA(testPriv)
	sign, _ := crypto.Sign(hash.Bytes(), key)
	voteA.Witness.Address = address
	voteA.Witness.Sign = sign
	return voteA
}

func getTestVm() *vm.VM {
	testCanTransfer := func(vm.StateDB, common.Address, *big.Int) bool {
		return true
	}
	testTransfer := func(vm.StateDB, common.Address, common.Address, *big.Int) {
		return
	}

	a := make(map[common.Address]*big.Int)
	c := make(map[common.Address][]byte)
	abi := make(map[common.Address][]byte)
	return vm.NewVM(vm.Context{
		BlockNumber: big.NewInt(1),
		CanTransfer: testCanTransfer,
		Transfer:    testTransfer,
		GasLimit:    model2.TxGas,
		GetHash:     getTestHashFunc(),
	}, fakeStateDB{account: a, code: c, abi: abi}, vm.DEFAULT_VM_CONFIG)
}

func getTestHashFunc() func(num uint64) common.Hash {
	return func(num uint64) common.Hash {
		return common.Hash{}
	}
}

func getContractCode(t *testing.T, code, abi string) []byte {
	fileCode, err := ioutil.ReadFile(code)
	assert.NoError(t, err)

	fileABI, err := ioutil.ReadFile(abi)
	assert.NoError(t, err)
	var input [][]byte
	input = make([][]byte, 0)
	// code
	input = append(input, fileCode)
	// abi
	input = append(input, fileABI)
	// params

	buffer := new(bytes.Buffer)
	err = rlp.Encode(buffer, input)
	assert.NoError(t, err)
	return buffer.Bytes()
}

func getContractInput(t *testing.T, funcName string, param [][]byte) []byte {
	var input [][]byte
	input = make([][]byte, 0)
	// func name
	input = append(input, []byte(funcName))
	// func parameter
	for _, v := range param {
		input = append(input, v)
	}

	buffer := new(bytes.Buffer)
	err := rlp.Encode(buffer, input)
	assert.NoError(t, err)
	return buffer.Bytes()
}

// get Contract data
func getCreateExtraData(wasmPath, abiPath string, params []string) (extraData []byte, err error) {
	// GetContractExtraData
	abiBytes, err := ioutil.ReadFile(abiPath)
	if err != nil {
		return
	}
	var wasmAbi utils.WasmAbi
	err = wasmAbi.FromJson(abiBytes)
	if err != nil {
		return
	}
	var args []utils.InputParam
	for _, v := range wasmAbi.AbiArr {
		if strings.EqualFold("init", v.Name) && strings.EqualFold(v.Type, "function") {
			args = v.Inputs
		}
	}
	//params := []string{"dipp", "DIPP", "100000000"}
	wasmBytes, err := ioutil.ReadFile(wasmPath)
	if err != nil {
		return
	}
	rlpParams := []interface{}{
		wasmBytes, abiBytes,
	}
	if len(params) != len(args) {
		return nil, errors.New("vm_utils: length of input and abi not match")
	}
	for i, v := range args {
		bts := params[i]
		re, innerErr := utils.StringConverter(bts, v.Type)
		if innerErr != nil {
			return nil, innerErr
		}
		rlpParams = append(rlpParams, re)
	}
	return rlp.EncodeToBytes(rlpParams)
}

//Get a test transaction
func getTestRegisterTransaction(nonce uint64, key *ecdsa.PrivateKey, amount *big.Int) *model.Transaction {
	trans := model.NewRegisterTransaction(nonce, amount, g_testData.TestGasPrice, g_testData.TestGasLimit)
	fs := model.NewMercurySigner(big.NewInt(1))
	signedTx, _ := trans.SignTx(key, fs)
	return signedTx
}

func getTestCancelTransaction(nonce uint64, key *ecdsa.PrivateKey) *model.Transaction {
	trans := model.NewCancelTransaction(nonce, g_testData.TestGasPrice, g_testData.TestGasLimit)
	fs := model.NewMercurySigner(big.NewInt(1))
	signedTx, _ := trans.SignTx(key, fs)
	return signedTx
}

func getTestUnStakeTransaction(nonce uint64, key *ecdsa.PrivateKey) *model.Transaction {
	trans := model.NewUnStakeTransaction(nonce, g_testData.TestGasPrice, g_testData.TestGasLimit)
	fs := model.NewMercurySigner(big.NewInt(1))
	signedTx, _ := trans.SignTx(key, fs)
	return signedTx
}

func getTestEvidenceTransaction(nonce uint64, key *ecdsa.PrivateKey, target common.Address, voteA, voteB *model.VoteMsg) *model.Transaction {
	trans := model.NewEvidenceTransaction(nonce, g_testData.TestGasPrice, g_testData.TestGasLimit, &target, voteA, voteB)
	fs := model.NewMercurySigner(big.NewInt(1))
	signedTx, _ := trans.SignTx(key, fs)
	return signedTx
}

type fakeStateStorage struct {
	getErr    error
	setErr    error
	passKey   string
	decodeErr bool
}

func (storage fakeStateStorage) OpenTrie(root common.Hash) (StateTrie, error) {
	return fakeTrie{
		getErr:    storage.getErr,
		setErr:    storage.setErr,
		passKey:   storage.passKey,
		decodeErr: storage.decodeErr,
	}, nil
}

func (storage fakeStateStorage) OpenStorageTrie(addrHash, root common.Hash) (StateTrie, error) {
	panic("implement me")
}

func (storage fakeStateStorage) CopyTrie(StateTrie) StateTrie {
	panic("implement me")
}

func (storage fakeStateStorage) TrieDB() *trie.Database {
	panic("implement me")
}

func (storage fakeStateStorage) DiskDB() ethdb.Database {
	return ethdb.NewMemDatabase()
}

type fakeTrie struct {
	getErr    error
	setErr    error
	passKey   string
	decodeErr bool
}

func (trie fakeTrie) TryGet(key []byte) ([]byte, error) {
	if trie.passKey == string(key[22:]) {
		return []byte{128}, nil
	}

	if trie.getErr != nil {
		return []byte{128}, trie.getErr
	}

	if trie.decodeErr {
		return []byte{1, 3}, nil
	}
	return []byte{128}, nil
}

func (trie fakeTrie) TryUpdate(key, value []byte) error {
	return trie.setErr
}

func (trie fakeTrie) TryDelete(key []byte) error {
	return TrieError
}

func (trie fakeTrie) Commit(onleaf trie.LeafCallback) (common.Hash, error) {
	return common.Hash{}, TrieError
}

func (trie fakeTrie) Hash() common.Hash {
	panic("implement me")
}

func (trie fakeTrie) NodeIterator(startKey []byte) trie.NodeIterator {
	panic("implement me")
}

func (trie fakeTrie) GetKey([]byte) []byte {
	panic("implement me")
}

func (trie fakeTrie) Prove(key []byte, fromLevel uint, proofDb ethdb.Putter) error {
	panic("implement me")
}

type erc20 struct {
	// todo special characters cause conversion errors
	Owners  []string            `json:"owne.rs"`
	Balance map[string]*big.Int `json:"balance"`
	Name    string              `json:"name"`
	Name2   string              `json:"name2"`
	Dis     uint64              `json:"dis"`
}

type fakeTransaction struct {
	txType common.TxType
	nonce  uint64
	err    error
	sender common.Address
}

func (tx fakeTransaction) PaddingReceipt(parameters model.ReceiptPara) (*model2.Receipt, error) {
	panic("implement me")
}

func (tx fakeTransaction) GetGasLimit() uint64 {
	return g_testData.TestGasLimit
}
func (tx fakeTransaction) GetReceipt() (*model2.Receipt, error) {
	panic("implement me")
}

func (tx fakeTransaction) PaddingTxIndex(index int) {
	panic("implement me")
}

func (tx fakeTransaction) GetTxIndex() (int, error) {
	panic("implement me")
}

func (tx fakeTransaction) AsMessage() (model.Message, error) {
	panic("implement me")
}

func (tx fakeTransaction) Size() common.StorageSize {
	panic("implement me")
}

func (tx fakeTransaction) GetGasPrice() *big.Int {
	return g_testData.TestGasPrice
}

func (tx fakeTransaction) Amount() *big.Int {
	return big.NewInt(10000)
}

func (tx fakeTransaction) CalTxId() common.Hash {
	return common.HexToHash("123")
}

func (tx fakeTransaction) Fee() *big.Int {
	return big.NewInt(40)
}

func (tx fakeTransaction) Nonce() uint64 {
	return tx.nonce
}

func (tx fakeTransaction) To() *common.Address {
	return &bobAddr
}

func (tx fakeTransaction) Sender(singer model.Signer) (common.Address, error) {
	return tx.sender, tx.err
}

func (tx fakeTransaction) SenderPublicKey(signer model.Signer) (*ecdsa.PublicKey, error) {
	panic("implement me")
}

func (tx fakeTransaction) EncodeRlpToBytes() ([]byte, error) {
	panic("implement me")
}

func (tx fakeTransaction) GetSigner() model.Signer {
	panic("implement me")
}

func (tx fakeTransaction) GetType() common.TxType {
	return tx.txType
}

func (tx fakeTransaction) ExtraData() []byte {
	c := erc20{}
	return util.StringifyJsonToBytes(c)
}

func (tx fakeTransaction) Cost() *big.Int {
	panic("implement me")
}

func (tx fakeTransaction) EstimateFee() *big.Int {
	panic("implement me")
}

type fakeStateDB struct {
	account map[common.Address]*big.Int
	code    map[common.Address][]byte
	abi     map[common.Address][]byte
}

func (state fakeStateDB) GetLogs(txHash common.Hash) []*model2.Log {
	panic("implement me")
}

func (state fakeStateDB) AddLog(addedLog *model2.Log) {
	log.Info("add log success")
	return
}

func (state fakeStateDB) CreateAccount(addr common.Address) {
	state.account[addr] = big.NewInt(1000)
}

func (state fakeStateDB) AddBalance(addr common.Address, amount *big.Int) {
	if state.account[addr] != nil {
		state.account[addr] = new(big.Int).Add(state.account[addr], amount)
	}
}

func (state fakeStateDB) SubBalance(addr common.Address, amount *big.Int) {
	state.account[addr] = new(big.Int).Sub(state.account[addr], amount)
}

func (state fakeStateDB) GetBalance(addr common.Address) *big.Int {
	if state.account[addr] == nil {
		state.account[addr] = big.NewInt(9000000)
	}
	return state.account[addr]
}

func (state fakeStateDB) GetNonce(common.Address) uint64 {
	return 0
}

func (state fakeStateDB) SetNonce(common.Address, uint64) {
	panic("implement me")
}

func (state fakeStateDB) AddNonce(common.Address, uint64) {
	return
}

func (state fakeStateDB) GetCodeHash(addr common.Address) common.Hash {
	code := state.code[addr]
	return cs_crypto.Keccak256Hash(code)
}

func (state fakeStateDB) GetCode(addr common.Address) []byte {
	return state.code[addr]
}

func (state fakeStateDB) SetCode(addr common.Address, code []byte) {
	state.code[addr] = code
}

func (state fakeStateDB) GetCodeSize(common.Address) int {
	panic("implement me")
}

func (state fakeStateDB) GetAbiHash(addr common.Address) common.Hash {
	return common.RlpHashKeccak256(state.GetAbi(addr))
}

func (state fakeStateDB) GetAbi(addr common.Address) []byte {
	return state.abi[addr]
}

func (state fakeStateDB) SetAbi(addr common.Address, abi []byte) {
	state.abi[addr] = abi
}

func (state fakeStateDB) GetCommittedState(common.Address, []byte) []byte {
	panic("implement me")
}

func (state fakeStateDB) GetState(common.Address, []byte) []byte {
	fmt.Println("fake stateDB get state sucessful")
	bytesBuffer := bytes.NewBuffer([]byte{})
	binary.Write(bytesBuffer, binary.LittleEndian, int32(123))
	return bytesBuffer.Bytes()
}

func (state fakeStateDB) SetState(common.Address, []byte, []byte) {
	fmt.Println("fake stateDB set state sucessful")
}

func (state fakeStateDB) Suicide(common.Address) bool {
	panic("implement me")
}

func (state fakeStateDB) HasSuicided(common.Address) bool {
	panic("implement me")
}

func (state fakeStateDB) Exist(common.Address) bool {
	return false
}

func (state fakeStateDB) Empty(common.Address) bool {
	panic("implement me")
}

func (state fakeStateDB) RevertToSnapshot(int) {
	panic("implement me")
}

func (state fakeStateDB) Snapshot() int {
	return 0
}
