package main

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/startfellows/tongo"
	"github.com/startfellows/tongo/boc"
	"github.com/startfellows/tongo/config"
	"github.com/startfellows/tongo/liteclient"
	"github.com/startfellows/tongo/tvm"
	"github.com/startfellows/tongo/wallet"
	"golang.org/x/crypto/pbkdf2"
	"io/ioutil"
	"log"
	"math/big"
	"os"
	"strings"
	"time"
)

const (
	WalletType = wallet.V3R2 // WRITE YOU WALLET TYPE HERE
)

type Config struct {
	Seed              string          `json:"seed"`
	CollectionAddress tongo.AccountID `json:"collection_address"`
	Domains           []struct {
		Name   string `json:"name"`
		MaxBid int64  `json:"max_bid"`
	} `json:"domains"`
}

type Domain struct {
	Name    string
	Address tongo.AccountID
	MaxBid  int64
}

type Auction struct {
	MaxBidAddress tongo.AccountID
	MaxBidAmount  int64
	EndTime       int64
}

type Worker struct {
	Client *liteclient.Client
	Domain Domain
	Wallet wallet.Wallet
}

func main() {
	conf, err := readConfig()
	if err != nil {
		log.Fatalf("Unable to read domain names from file: %v", err)
	}

	options, err := config.ParseConfigFile("global-config.json")
	if err != nil {
		log.Fatalf("Unable to load network config: %v", err)
	}

	client, err := liteclient.NewClient(options)
	if err != nil {
		log.Fatalf("Unable to create lite client: %v", err)
	}

	collectionState, err := client.GetLastRawAccountState(context.Background(), conf.CollectionAddress)
	if err != nil {
		log.Fatalf("Unable to get collection state: %v", err)
	}

	pk, err := seedToPrivateKey(conf.Seed)
	if err != nil {
		log.Fatalf("Unable to get private key from seed: %v", err)
	}
	privateKey := ed25519.NewKeyFromSeed(pk)

	w, err := wallet.NewWallet(privateKey, WalletType, 0, nil)
	if err != nil {
		log.Fatalf("Unable to create wallet: %v", err)
	}
	log.Printf("You wallet address: %v\n", w.GetAddress().ToHuman(true, false))

	for _, domain := range conf.Domains {
		addr, err := getItemAddressByTvm(domain.Name, collectionState.Code, collectionState.Data, conf.CollectionAddress)
		if err != nil {
			log.Fatalf("unable to get domain %v address: %v", domain.Name, err)
		}
		log.Printf("Domain \"%v\" contract address: %v\n", domain.Name, addr.ToHuman(true, false))
		dom := Domain{Address: addr, Name: domain.Name, MaxBid: domain.MaxBid}
		worker := Worker{
			Domain: dom,
			Client: client,
			Wallet: w,
		}
		go worker.start()
		time.Sleep(time.Minute)
	}

	for {
		time.Sleep(time.Hour)
	}
}

func readConfig() (Config, error) {
	jsonFile, err := os.Open("config.json")
	if err != nil {
		return Config{}, err
	}
	defer jsonFile.Close()
	byteValue, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		return Config{}, err
	}
	var conf Config
	err = json.Unmarshal(byteValue, &conf)
	if err != nil {
		return Config{}, err
	}
	return conf, nil
}

func getItemAddressByTvm(name string, code, data []byte, address tongo.AccountID) (tongo.AccountID, error) {
	codeCell, err := boc.DeserializeBoc(code)
	if err != nil {
		return tongo.AccountID{}, err
	}
	dataCell, err := boc.DeserializeBoc(data)
	if err != nil {
		return tongo.AccountID{}, err
	}
	cell := boc.NewCell()
	err = cell.WriteBytes([]byte(name))
	if err != nil {
		return tongo.AccountID{}, err
	}
	hash, err := cell.Hash()
	if err != nil {
		return tongo.AccountID{}, err
	}
	index := new(big.Int)
	index.SetBytes(hash[:])
	args := []tvm.StackEntry{
		tvm.NewBigIntStackEntry(*index),
	}
	result, err := tvm.RunTvm(codeCell[0], dataCell[0], "get_nft_address_by_index", args, &address)
	if err != nil {
		return tongo.AccountID{}, err
	}
	if result.ExitCode != 0 && result.ExitCode != 1 { // 1 - alternative success code
		return tongo.AccountID{}, fmt.Errorf("TVM execution failed")
	}
	if len(result.Stack) != 1 || !result.Stack[0].IsCellSlice() {
		return tongo.AccountID{}, fmt.Errorf("invalid stack data")
	}
	aa, err := tongo.AccountIDFromCell(result.Stack[0].CellSlice())
	if err != nil {
		return tongo.AccountID{}, err
	}
	if aa == nil {
		return tongo.AccountID{}, fmt.Errorf("empty domain address")
	}
	return *aa, nil
}

func (w Worker) start() {
	log.Printf("Domain \"%v\" worker started", w.Domain.Name)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		state, err := w.Client.GetLastRawAccountState(ctx, w.Domain.Address)
		if err != nil {
			log.Printf("get account state failed for %v: %v\n", w.Domain.Name, err)
			time.Sleep(time.Minute)
			continue
		}
		auction, err := getAuctionStatus(state, &w.Domain.Address)
		if err != nil {
			log.Printf("get auction status failed for %v: %v\n", w.Domain.Name, err)
			time.Sleep(time.Minute)
			continue
		}
		if time.Now().Unix() > auction.EndTime {
			log.Printf("auction finished for: %v\n", w.Domain.Name)
			break
		}
		if auction.MaxBidAddress.ToRaw() != w.Wallet.GetAddress().ToRaw() {
			err = w.placeBid(auction)
			if err != nil {
				log.Printf("place bid error for %v: %v\n", w.Domain.Name, err)
				time.Sleep(time.Minute)
				continue
			}
		}
		time.Sleep(time.Minute * 5)
	}
}

func getAuctionStatus(state liteclient.AccountState, account *tongo.AccountID) (Auction, error) {
	codeCell, err := boc.DeserializeBoc(state.Code)
	if err != nil {
		return Auction{}, err
	}
	dataCell, err := boc.DeserializeBoc(state.Data)
	if err != nil {
		return Auction{}, err
	}
	args := make([]tvm.StackEntry, 0)
	result, err := tvm.RunTvm(codeCell[0], dataCell[0], "get_auction_info", args, account)
	if result.ExitCode != 0 && result.ExitCode != 1 { // 1 - alternative success code
		return Auction{}, fmt.Errorf("TVM execution failed")
	}
	if len(result.Stack) != 3 {
		return Auction{}, fmt.Errorf("invalid stack data")
	}
	if result.Stack[0].IsNull() {
		// TODO: auction not available
		return Auction{}, fmt.Errorf("auction not available")
	}
	aa, err := tongo.AccountIDFromCell(result.Stack[0].CellSlice())
	if err != nil {
		return Auction{}, err
	}
	if aa == nil {
		return Auction{}, fmt.Errorf("empty max bid address")
	}
	return Auction{
		MaxBidAddress: *aa,
		MaxBidAmount:  result.Stack[1].Int64(),
		EndTime:       result.Stack[2].Int64(),
	}, nil
}

func (w Worker) placeBid(auction Auction) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	state, err := w.Client.GetLastRawAccountState(ctx, w.Wallet.GetAddress())
	if err != nil {
		return err
	}
	bid := int64(float64(auction.MaxBidAmount) * 1.051)
	if int64(state.Balance) < bid {
		return fmt.Errorf("not enough coins for bid")
	}
	if bid > w.Domain.MaxBid {
		return fmt.Errorf("bid limit reached")
	}
	err = w.pay(bid)
	if err != nil {
		return err
	}
	log.Printf("You place new bid %.3f TON for \"%v\"\n", float64(bid)/1_000_000_000, w.Domain.Name)
	return nil
}

func (w Worker) pay(amount int64) error {
	tonTransfer := wallet.TonTransfer{
		Recipient: w.Domain.Address,
		Amount:    tongo.Grams(amount),
		Comment:   "bid",
		Bounce:    true,
		Mode:      1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	res, err := w.Client.RunSmcMethod(ctx, 4, w.Wallet.GetAddress(), "seqno", tongo.VmStack{})
	if err != nil {
		return fmt.Errorf("unable to get seqno: %v", err)
	}
	msg, err := w.Wallet.GenerateTonTransferMessage(uint32(res[0].VmStkTinyInt), 0xFFFFFFFF, []wallet.TonTransfer{tonTransfer})
	if err != nil {
		return fmt.Errorf("unable to generate transfer message: %v", err)
	}
	err = w.Client.SendRawMessage(ctx, msg)
	if err != nil {
		return fmt.Errorf("send message error: %v", err)
	}
	return nil
}

func seedToPrivateKey(s string) ([]byte, error) {
	seed := strings.Split(s, " ")
	if len(seed) < 12 {
		return nil, fmt.Errorf("seed should have at least 12 words")
	}
	mac := hmac.New(sha512.New, []byte(strings.Join(seed, " ")))
	hash := mac.Sum(nil)
	p := pbkdf2.Key(hash, []byte("TON seed version"), 100000/256, 1, sha512.New)
	if p[0] != 0 {
		return nil, errors.New("invalid seed")
	}
	k := pbkdf2.Key(hash, []byte("TON default seed"), 100000, 32, sha512.New)
	return k, nil
}
