package librelay

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"gen/librelay"
	"gen/samplerec"
	"librelay/test"
	"librelay/txstore"
	"log"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/clock/fakeclock"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

type TestClient struct {
	*ethclient.Client

	RPC *rpc.Client
}

func NewTestClient(url string) (*TestClient, error) {
	rpcClient, err := rpc.DialContext(context.Background(), url)
	if err != nil {
		return nil, err
	}

	return &TestClient{
		ethclient.NewClient(rpcClient),
		rpcClient,
	}, nil
}

func (client *TestClient) AdjustTime(seconds uint64) error {
	return client.RPC.Call(nil, "evm_increaseTime", seconds)
}

func (client *TestClient) Commit() error {
	return client.RPC.Call(nil, "evm_mine")
}

func (client *TestClient) MineBlocks(n uint64) error {
	for ; n > 0; n-- {
		err := client.RPC.Call(nil, "evm_mine")
		if err != nil {
			return err
		}
	}
	return nil
}

func (client *TestClient) Snapshot() (uint64, error) {
	var result hexutil.Uint64
	err := client.RPC.Call(&result, "evm_snapshot")
	return uint64(result), err
}

func (client *TestClient) Revert(id uint64) error {
	return client.RPC.Call(nil, "evm_revert", id)
}

type TestServer struct {
	*RelayServer
}

func (relay *TestServer) Stake(ownerKey *ecdsa.PrivateKey, stakeAmount *big.Int, unstakeDelay *big.Int) (err error) {
	tx, err := relay.sendStakeTransaction(ownerKey, stakeAmount, unstakeDelay)
	if err != nil {
		return err
	}
	return relay.awaitTransactionMined(tx)
}

func (relay *TestServer) sendStakeTransaction(ownerKey *ecdsa.PrivateKey, stakeAmount *big.Int, unstakeDelay *big.Int) (tx *types.Transaction, err error) {
	auth := bind.NewKeyedTransactor(ownerKey)
	auth.Value = stakeAmount
	tx, err = relay.rhub.Stake(auth, relay.Address(), unstakeDelay)
	if err != nil {
		log.Println("rhub.stake() failed", stakeAmount, unstakeDelay)
		return
	}
	log.Println("Stake() tx sent:", tx.Hash().Hex())
	return
}

func (relay *TestServer) Unstake(ownerKey *ecdsa.PrivateKey, stakeAmount *big.Int) (err error) {
	tx, err := relay.sendUnstakeTransaction(ownerKey, stakeAmount)
	if err != nil {
		return err
	}
	return relay.awaitTransactionMined(tx)

}

func (relay *TestServer) sendUnstakeTransaction(ownerKey *ecdsa.PrivateKey, stakeAmount *big.Int) (tx *types.Transaction, err error) {
	auth := bind.NewKeyedTransactor(ownerKey)
	auth.Value = stakeAmount
	tx, err = relay.rhub.Unstake(auth, relay.Address())
	if err != nil {
		log.Println("rhub.Unstake() failed", stakeAmount)
		return
	}
	log.Println("Unstake() tx sent:", tx.Hash().Hex())
	return
}

var auth *bind.TransactOpts
var relay TestServer
var client *TestClient
var relayKey1 *ecdsa.PrivateKey
var gaslessKey2 *ecdsa.PrivateKey
var ownerKey3 *ecdsa.PrivateKey
var unstakeDelay = big.NewInt(1 * 60 * 60 * 24 * 7) // 1 week in seconds
var stakeAmount = big.NewInt(1100000000000000000)
var rhub *librelay.IRelayHub
var clk *fakeclock.FakeClock

var sampleRecipient common.Address
var rhaddr common.Address

var boundHub *bind.BoundContract
var boundRecipient *bind.BoundContract

var ethereumNodeURL = "http://localhost:8543"

func InitTestClient(url string) {
	relayKey1, _ = crypto.HexToECDSA("4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7d21715b23b1d")
	gaslessKey2, _ = crypto.HexToECDSA("6cbed15c793ce57650b9877cf6fa156fbef513c4e6134f022a85b1ffdd59b2a1")
	ownerKey3, _ = crypto.HexToECDSA("6370fd033278c143179d81c5526140625662b8daa446c22ee2d73db3707e620c")

	fmt.Println("Test addresses:")
	fmt.Println("1. Relay  ", crypto.PubkeyToAddress(relayKey1.PublicKey).Hex())
	fmt.Println("2. Gasless", crypto.PubkeyToAddress(gaslessKey2.PublicKey).Hex())
	fmt.Println("3. Owner  ", crypto.PubkeyToAddress(ownerKey3.PublicKey).Hex())

	auth = bind.NewKeyedTransactor(relayKey1)
	var err error
	client, err = NewTestClient(url)
	if err != nil {
		log.Fatalf("Could not connect to local ganache: %v", err)
	}
	client.Commit()
}

func NewRelay(relayHubAddress common.Address) {
	fee := big.NewInt(10)
	defaultGasPrice := int64(params.GWei)
	gasPricePercent := big.NewInt(10)
	url := ""
	port := "8090"
	registrationBlockRate := uint64(5)
	clk = fakeclock.NewFakeClock(time.Now())
	txStore := txstore.NewMemoryTxStore(clk)
	devMode := false
	var err error
	relay.RelayServer, err = NewRelayServer(
		common.Address{}, fee, url, port,
		relayHubAddress, defaultGasPrice,
		gasPricePercent, relayKey1, registrationBlockRate,
		ethereumNodeURL, client, txStore, clk, devMode)
	if err != nil {
		log.Fatalln("Relay was not created", err)
	}
	return
}

func TestMain(m *testing.M) {
	InitTestClient(ethereumNodeURL)
	parsed, err := abi.JSON(strings.NewReader(librelay.IRelayHubABI))
	if err != nil {
		log.Fatalln(err)
	}

	RelayHubBin := librelay.IRelayHubBin

	if _, err = hex.DecodeString(RelayHubBin[2:]); err != nil {
		log.Println("RelayHubBin", RelayHubBin)
		log.Fatalln("Invalid hex: RelayHubBin", err)
	}
	auth.GasLimit = 8000000
	rhaddr, _, boundHub, err = bind.DeployContract(auth, parsed, common.FromHex(RelayHubBin), client)
	if err != nil {
		log.Fatalf("could not deploy contract: %v", err)
	}
	parsed, err = abi.JSON(strings.NewReader(samplerec.SampleRecipientABI))
	if err != nil {
		log.Fatalln(err)
	}
	auth.GasLimit = 4000000
	sampleRecipient, _, boundRecipient, err = bind.DeployContract(auth, parsed, common.FromHex(samplerec.SampleRecipientBin), client)
	if err != nil {
		log.Fatalln("Error deploying SampleRecipient contract:", err)
	}
	sr, err :=samplerec.NewSampleRecipient(sampleRecipient, client)
	if err != nil {
		log.Fatalln(err)
	}
	_,err = sr.SetHub(auth, rhaddr)
	if err != nil {
		log.Fatalln(err)
	}

	rhub, err = librelay.NewIRelayHub(rhaddr, client)
	if err != nil {
		log.Fatalln(err)
	}
	fmt.Printf("RelayHub:  %s\nRecipient: %s\n", rhaddr.String(), sampleRecipient.String())
	NewRelay(rhaddr)

	tx, err := relay.sendStakeTransaction(ownerKey3, stakeAmount, unstakeDelay)
	if err != nil {
		log.Fatalf("Could not 'sendStakeTransaction': %v", err)
	}
	client.Commit()
	err = relay.awaitTransactionMined(tx)
	if err != nil {
		log.Fatalln(err)
	}

	auth := bind.NewKeyedTransactor(ownerKey3)
	auth.Value = big.NewInt(1)
	auth.Value.Lsh(auth.Value, 40)

	tx, err = rhub.DepositFor(auth, sampleRecipient)
	client.Commit()
	if err != nil {
		log.Fatalln(err)
	}
	_, _ = client.TransactionReceipt(context.Background(), tx.Hash())

	callOpt := &bind.CallOpts{}
	toBalance, err := rhub.BalanceOf(callOpt, sampleRecipient)
	if err != nil {
		log.Println(err)
		return
	}
	log.Println("To.balance: ", toBalance)

	fmt.Println("-----------------------------------------------------")
	flag.Parse()
	exitStatus := m.Run()
	defer os.Exit(exitStatus)
}

func TestRefreshGasPrice(t *testing.T) {
	gasPriceBefore := relay.GasPrice()
	test.ErrFail(relay.RefreshGasPrice(), t)
	gasPriceAfter := relay.GasPrice()
	if gasPriceBefore.Cmp(big.NewInt(0)) != 0 {
		t.Error()
	}
	// Gas price is ganache default plus 10% specified in relay constructor
	if gasPriceAfter.Cmp(big.NewInt(1100)) != 0 {
		t.Error("Gas price after is", gasPriceAfter.Uint64())
	}
}

func TestRegisterRelay(t *testing.T) {
	staked, err := relay.IsStaked()
	if !staked {
		t.Error("Relay is not staked")
	}
	test.ErrFail(err, t)
	// TODO: Watch out for FLICKERING: attempt to AdjustTime ahead of machine clock will have no effect at all
	err = client.AdjustTime(50)
	client.Commit()
	tx, err := relay.sendRegisterTransaction()
	test.ErrFail(err, t)
	if err != nil {
		fmt.Println("ERROR", err)
	}
	client.Commit()
	test.ErrFail(relay.awaitTransactionMined(tx), t)
	count, err := relay.BlockCountSinceRegistration()
	if err != nil {
		fmt.Println("ERROR", err)
	}
	if count > 1 {
		t.Error("Wrong registration block",  count)
	}
}

func printSignature(txb string, txFee int64, gasPrice int64, gasLimit int64, relayMaxNonce int64, recipientNonce int64) {
	fmt.Println("ganache-cli -d")
	fmt.Println("npx truffle console --network development")
	fmt.Println("const utils = require('../src/js/relayclient/utils')")
	//fmt.Printf(
	//	"let hash = utils.getTransactionHash('%v', '%v', '%v', '%v', '%v', '%v', '%v', '%v', '%v')\n",
	//	crypto.PubkeyToAddress(gaslessKey2.PublicKey).Hex(), sampleRecipient.Hex(), txb, txFee, gasPrice, gasLimit, recipientNonce, rhaddr.Hex(), relay.Address().Hex(),
	//)
	//fmt.Printf("utils.getTransactionSignature(web3, '0xffcf8fdee72ac11b5c542428b35eef5769c409f0', hash)\n")
	fmt.Printf(
		"let hash = utils.getEip712Signature({web3, senderAccount: '%v', senderNonce: '%v', target: '%v', encodedFunction: '%v', pctRelayFee: '%v', gasPrice: '%v', gasLimit: '%v', relayHub: '%v', relayAddress: '%v'})\n",
		crypto.PubkeyToAddress(gaslessKey2.PublicKey).Hex(), recipientNonce, sampleRecipient.Hex(), txb, txFee, gasPrice, gasLimit, rhaddr.Hex(), relay.Address().Hex(),
	)
}

func newRelayTransactionRequest(t *testing.T, recipientNonce int64, signature string) (request RelayTransactionRequest) {
	test.ErrFail(relay.RefreshGasPrice(), t)
	addressGasless := crypto.PubkeyToAddress(gaslessKey2.PublicKey)
	txb := "0x2ac0df260000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000000b68656c6c6f20776f726c64000000000000000000000000000000000000000000"
	txFee := int64(10)
	gasPrice := int64(2000)
	gasLimit := int64(1000000)
	relayMaxNonce := int64(1000000)
	if signature[:2] == "0x" {
		signature = signature[2:]
	}

	// Uncomment the following line to print the commands to generate the signature that needs to be injected
	//printSignature(txb, txFee, gasPrice, gasLimit, relayMaxNonce, recipientNonce)

	return RelayTransactionRequest{
		EncodedFunction: txb,
		ApprovalData:    common.Hex2Bytes(""),
		Signature:       common.Hex2Bytes(signature),
		From:            addressGasless,
		To:              sampleRecipient,
		GasPrice:        *big.NewInt(gasPrice),
		GasLimit:        *big.NewInt(gasLimit),
		RecipientNonce:  *big.NewInt(recipientNonce),
		RelayMaxNonce:   *big.NewInt(relayMaxNonce),
		RelayFee:        *big.NewInt(txFee),
		RelayHubAddress: rhaddr,
	}
}

func assertTransactionRelayed(t *testing.T, txHash common.Hash) (receipt *types.Receipt) {
	receipt, err := client.TransactionReceipt(context.Background(), txHash)
	test.ErrFailWithDesc(err, t, fmt.Sprint("Fetching transaction receipt for hash ", txHash.Hex()))
	logsLen := len(receipt.Logs)
	expectedLogs := 4
	if logsLen != expectedLogs {
		t.Errorf("Incorrect logs len: expected %d, actual: %d", expectedLogs, logsLen)
	}
	transactionRelayedEvent := new(librelay.IRelayHubTransactionRelayed)
	sampleRecipientEmitted := new(samplerec.SampleRecipientSampleRecipientEmitted)
	preRelayedEmitted := new(samplerec.SampleRecipientSampleRecipientPreCall)
	postRelayedEmitted := new(samplerec.SampleRecipientSampleRecipientPostCall)
	test.ErrFailWithDesc(boundRecipient.UnpackLog(preRelayedEmitted, "SampleRecipientPreCall", *receipt.Logs[0]), t, "Unpacking SampleRecipientPreCall")
	test.ErrFailWithDesc(boundRecipient.UnpackLog(sampleRecipientEmitted, "SampleRecipientEmitted", *receipt.Logs[1]), t, "Unpacking sample recipient emitted")
	test.ErrFailWithDesc(boundRecipient.UnpackLog(postRelayedEmitted, "SampleRecipientPostCall", *receipt.Logs[2]), t, "Unpacking SampleRecipientPostCall")
	test.ErrFailWithDesc(boundHub.UnpackLog(transactionRelayedEvent, "TransactionRelayed", *receipt.Logs[3]), t, "Unpacking transaction relayed")

	expectedMessage := "hello world"
	if sampleRecipientEmitted.Message != expectedMessage {
		t.Errorf("Message was not what expected! expected: %s actual: %s", expectedMessage, sampleRecipientEmitted.Message)
	}
	return receipt
}

func assertRelayNonce(t *testing.T, expected uint64) {
	nonce, err := client.NonceAt(context.Background(), relay.Address(), nil)
	if nonce != expected || err != nil {
		t.Errorf("Relay nonce is %v but expected %v (error %v)", nonce, expected, err)
	}
}

func assertNoTransactionResent(t *testing.T, relay *RelayServer) {
	noTx, err := relay.UpdateUnconfirmedTransactions()
	test.ErrFailWithDesc(err, t, "Updating unconfirmed transactions")
	if noTx != nil {
		t.Errorf("Expected no tx to be resent upon updating unconfirmed txs, but %v with nonce %v was resent", noTx.Hash().Hex(), noTx.Nonce())
	}
}

func TestCreateRelayTransaction(t *testing.T) {
	request := newRelayTransactionRequest(t, 0, "0xd735f61c4364b4543fe627529959558ea14f7d50940347c2073db1ef1d18691958be8c4da835d65bb84e74eff73c428085dc1b3fce14b34fb024e27c4e6921391c")
	signedTx, err := relay.CreateRelayTransaction(request)
	test.ErrFailWithDesc(err, t, "Creating relay transaction")
	client.Commit()
	assertTransactionRelayed(t, signedTx.Hash())
}

func TestResendRelayTransaction(t *testing.T) {
	test.ErrFail(relay.TxStore.Clear(), t)
	request := newRelayTransactionRequest(t, 1, "0xfcddc648afc3aa23da5e867fe2d5ca04b8982dd30a52245d62690bab5647a70e00700e75fc6b386edf4aff42abaf6fefe4c37965831bab1fa5091fe840a8b3721b")

	// Send a transaction via the relay, but then revert to a previous snapshot
	snapshotID, err := client.Snapshot()
	test.ErrFailWithDesc(err, t, "Creating snapshot")
	signedTx, err := relay.CreateRelayTransaction(request)
	test.ErrFailWithDesc(err, t, "Creating relay transaction")
	err = client.Revert(snapshotID)
	test.ErrFailWithDesc(err, t, "Restoring snapshot")

	// Ensure tx is removed by the revert
	_, err = client.TransactionReceipt(context.Background(), signedTx.Hash())
	if err != ethereum.NotFound {
		t.Errorf("Transaction %v should not have been found (error %v)", signedTx.Hash().Hex(), err)
	}

	// Should not do anything, as not enough time has passed
	clk.IncrementBySeconds(1 * 60)
	assertNoTransactionResent(t, relay.RelayServer)
	sameTx, err := relay.TxStore.GetFirstTransaction()
	if sameTx.Hash() != signedTx.Hash() {
		t.Errorf("Transaction should not have been resent if less than 5 minutes passed: original tx %v but loaded from store was %v", signedTx.Hash().Hex(), sameTx.Hash().Hex())
	}
	_, err = client.TransactionReceipt(context.Background(), sameTx.Hash())
	if err != ethereum.NotFound {
		t.Errorf("Transaction %v should not have been found (error %v)", sameTx.Hash().Hex(), err)
	}

	// Advance time
	clk.IncrementBySeconds(6 * 60)
	newTx, err := relay.UpdateUnconfirmedTransactions()
	test.ErrFailWithDesc(err, t, "Updating unconfirmed transactions")

	// Check transaction was now sent with increased gas price
	client.MineBlocks(2)
	assertTransactionRelayed(t, newTx.Hash())
	if newTx.GasPrice().Int64() != 2400 {
		t.Errorf("Gas price of resent transaction is incorrect: expected %v but was %v", 2400, newTx.GasPrice().Int64())
	}

	// Check the tx is removed from the store after enough blocks
	client.MineBlocks(12)
	assertNoTransactionResent(t, relay.RelayServer)
	missingTx, err := relay.TxStore.GetFirstTransaction()
	if missingTx != nil || err != nil {
		t.Errorf("Transaction %v was not removed from store after 12 confirmations (error %v)", missingTx.Hash().Hex(), err)
	}
}

func TestMultipleRelayTransactions(t *testing.T) {
	test.ErrFail(relay.TxStore.Clear(), t)
	request1 := newRelayTransactionRequest(t, 2, "0x3059a03a43ff3f72cf80446ba5c3ea9a6d9f9a959cc2eb8fa32d229484e9bec42d661d7591416d6be4d1022ee8da3c13d2c38832318491071f27ad150e813cd41b")
	request2 := newRelayTransactionRequest(t, 3, "0xd0d7692130a1bf69c75828730f24a7898cdfc11f0085350297c4b2092c0e321e16bce77cdf98150b7aefd2fe68f1bf0e2e488663fa02ef3c4baaf681d8acac401c")
	request3 := newRelayTransactionRequest(t, 4, "0x8ef49334c17c5a2aee322a834c5ee25b6e6882e3c51c6eac661671d1f0fdc96137f93731be9d7365a7640bd97280c4b9260fc25637700b7d810343bcc4cebb9e1c")

	// Send 3 transactions, separated by 1 min each, and revert the last 2
	signedTx1, err := relay.CreateRelayTransaction(request1)
	test.ErrFailWithDesc(err, t, "Creating relay transaction 1")
	clk.IncrementBySeconds(60)
	snapshotID, err := client.Snapshot()
	test.ErrFailWithDesc(err, t, "Creating snapshot")
	_, err = relay.CreateRelayTransaction(request2)
	test.ErrFailWithDesc(err, t, "Creating relay transaction 2")
	clk.IncrementBySeconds(60)
	signedTx3, err := relay.CreateRelayTransaction(request3)
	test.ErrFailWithDesc(err, t, "Creating relay transaction 3")
	err = client.Revert(snapshotID)
	test.ErrFailWithDesc(err, t, "Restoring snapshot")
	nonce, err := client.NonceAt(context.Background(), relay.Address(), nil)

	// Check tx1 went fine
	assertTransactionRelayed(t, signedTx1.Hash())

	// After 5 minutes, tx2 is not resent because tx1 is still unconfirmed
	clk.IncrementBySeconds(60 * 5)
	assertNoTransactionResent(t, relay.RelayServer)
	assertRelayNonce(t, nonce)

	// Mine a bunch of blocks, so tx1 is confirmed and tx2 is resent
	client.MineBlocks(12)
	newTx2, err := relay.UpdateUnconfirmedTransactions()
	test.ErrFailWithDesc(err, t, "Updating unconfirmed transactions")
	assertRelayNonce(t, nonce+1)
	assertTransactionRelayed(t, newTx2.Hash())

	// Reinject tx3 into the chain as if it were mined once tx2 goes through
	test.ErrFailWithDesc(client.SendTransaction(context.Background(), signedTx3), t, "Resending tx3")
	assertTransactionRelayed(t, signedTx3.Hash())

	// Check that tx3 does not get resent, even after time passes or blocks get mined, and that store is empty
	assertNoTransactionResent(t, relay.RelayServer)
	clk.IncrementBySeconds(300)
	client.MineBlocks(12)
	assertNoTransactionResent(t, relay.RelayServer)
	noTx, err := relay.TxStore.GetFirstTransaction()
	if noTx != nil || err != nil {
		t.Errorf("Expected tx store to be empty but found %v (error %v)", noTx, err)
	}
}

func TestReuseNonceOnDevMode(t *testing.T) {
	test.ErrFail(relay.TxStore.Clear(), t)
	request := newRelayTransactionRequest(t, 5, "0x36cc44c4492105b6b8d025bc674b16b277a2d200b2e0598cf991d063b3f816d452fc547c39176a2f0e4d3e82bf84285ba9db5f79e8bb69e431d6a6523c4ff8b91b")

	// Relay a tx
	snapshotID, err := client.Snapshot()
	test.ErrFailWithDesc(err, t, "Creating snapshot")
	signedTx1, err := relay.CreateRelayTransaction(request)
	assertTransactionRelayed(t, signedTx1.Hash())

	// Revert blockchain state and resend it, failing with "the tx doesn't have the correct nonce"
	test.ErrFailWithDesc(client.Revert(snapshotID), t, "Restoring snapshot")
	noTx, err := relay.CreateRelayTransaction(request)
	if noTx != nil || err == nil {
		t.Errorf("Expected relay operation to fail due to nonce")
	}

	// Disable nonce cache and retry successfully
	relay.DevMode = true
	signedTx2, err := relay.CreateRelayTransaction(request)
	test.ErrFailWithDesc(err, t, "Sending tx with old nonce on dev mode")
	assertTransactionRelayed(t, signedTx2.Hash())

	// Clean up for the next test
	relay.DevMode = false
}

func TestTransactionTotalGasCost(t *testing.T) {
	test.ErrFail(relay.TxStore.Clear(), t)
	// We create two relayed txs of equal byte length, with the second tx having one more non-zero byte and one less zero byte
	// meaning that the expected gas cost diff between them is 64 per the yellowpaper
	expectedGasDiff := uint64(64)

	request1 := newRelayTransactionRequest(t, 6, "0xda24ee9c19adfb9343cc7b78ae3c3a75fc9f9d13fb7d2eef1b24d0a372b1cef624764209319f9ee3465b54822f3ab59c2d9a348496e0d25af047ab917e9267c01b")
	// Changing encoded function to call dontEmitMessage() instead of emitMessage()
	request1.EncodedFunction = "0xb51fab0a0000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000000b68656c6c6f20776f726c64000000000000000000000000000000000000000000"
	// Creating a new relayed tx with encoded function 'dontEmitMessage("hello world" + "\x00" + "a")' instead of 'dontEmitMessage("hello world")'
	request2 := newRelayTransactionRequest(t, 7, "0xf8083635cfe641ae7c2f6ec6d2815d6ba9b4fec05fcdf4ee50344d22a8e54da640336912eef27b933eea5ecc94ed7b91534a91fe577510bb3febf9bc6610c1201c")
	request2.EncodedFunction = "0xb51fab0a0000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000000d68656c6c6f20776f726c64610000000000000000000000000000000000000000"

	// Send the 2 transactions
	signedTx1, err := relay.CreateRelayTransaction(request1)
	test.ErrFailWithDesc(err, t, "Creating relay transaction 1")
	clk.IncrementBySeconds(60)
	receipt1, err := client.TransactionReceipt(context.Background(), signedTx1.Hash())
	test.ErrFailWithDesc(err, t, fmt.Sprint("Fetching transaction receipt for hash ", signedTx1.Hash()))
	signedTx2, err := relay.CreateRelayTransaction(request2)
	test.ErrFailWithDesc(err, t, "Creating relay transaction 2")
	clk.IncrementBySeconds(60)
	receipt2, err := client.TransactionReceipt(context.Background(), signedTx2.Hash())
	test.ErrFailWithDesc(err, t, fmt.Sprint("Fetching transaction receipt for hash ", signedTx2.Hash()))

	if  getEncodedFunctionGas(request2.EncodedFunction).Uint64() - getEncodedFunctionGas(request1.EncodedFunction).Uint64() != expectedGasDiff {
		errStr := fmt.Sprintf("Wrong gas cost difference between encoded functions:\nrequest2: %v, request1: %v", getEncodedFunctionGas(request2.EncodedFunction).Uint64(), getEncodedFunctionGas(request1.EncodedFunction).Uint64())
		test.ErrFail(errors.New(errStr), t)
	}
	if receipt2.GasUsed - receipt1.GasUsed != expectedGasDiff {
		errStr := fmt.Sprintf("Wrong gasUsed difference between relayed transactions:\nreceipt2.GasUsed: %v, receipt1.GasUsed: %v", receipt2.GasUsed, receipt1.GasUsed)
		// TODO fix gas calculation
		t.Logf(errStr)
	}
}

func TestGetEncodedFunctionGas(t *testing.T) {
	encodedFunction := "2ac0df260000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000000b68656c6c6f20776f726c64000000000000000000000000000000000000000000"
	gas := getEncodedFunctionGas(encodedFunction)
	if gas.Cmp(big.NewInt(1488)) != 0 {
		test.ErrFail(errors.New("Wrong gas calculation"), t)
	}
	encodedFunctionWithPrefix := "0x" + encodedFunction
	gas = getEncodedFunctionGas(encodedFunctionWithPrefix)
	if gas.Cmp(big.NewInt(1488)) != 0 {
		test.ErrFail(errors.New("Wrong gas calculation"), t)
	}
}
