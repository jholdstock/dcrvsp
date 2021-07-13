// Copyright (c) 2020-2021 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package webapi

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/decred/dcrd/chaincfg/v3"
	dcrdtypes "github.com/decred/dcrd/rpc/jsonrpc/types/v3"
	"github.com/decred/slog"
	"github.com/decred/vspd/database"
	"github.com/gin-gonic/gin"
)

const (
	addrCharset = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	hexCharset  = "1234567890abcdef"
	testDb      = "test.db"
	backupDb    = "test.db-backup"
)

var (
	seededRand           = rand.New(rand.NewSource(time.Now().UnixNano()))
	feeXPub              = "feexpub"
	maxVoteChangeRecords = 3
)

func TestMain(m *testing.M) {
	// Set test logger to stdout.
	backend := slog.NewBackend(os.Stdout)
	log = backend.Logger("test")
	log.SetLevel(slog.LevelTrace)

	// Set up some global params.
	cfg.NetParams = chaincfg.MainNetParams()
	_, signPrivKey, _ = ed25519.GenerateKey(seededRand)

	// Create a database to use.
	// Ensure we are starting with a clean environment.
	os.Remove(testDb)
	os.Remove(backupDb)

	// Create a new blank database for each sub-test.
	var err error
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	err = database.CreateNew(testDb, feeXPub)
	if err != nil {
		panic(fmt.Errorf("error creating test database: %v", err))
	}
	db, err = database.Open(ctx, &wg, testDb, time.Hour, maxVoteChangeRecords)
	if err != nil {
		panic(fmt.Errorf("error opening test database: %v", err))
	}

	// Run tests.
	exitCode := m.Run()

	// Request database shutdown and wait for it to complete.
	cancel()
	wg.Wait()

	db.Close()

	os.Remove(testDb)
	os.Remove(backupDb)

	os.Exit(exitCode)
}

// randString randomly generates a string of the requested length, using only
// characters from the provided charset.
func randString(length int, charset string) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(b)
}

func exampleTicket() database.Ticket {
	const hexCharset = "1234567890abcdef"

	return database.Ticket{
		Hash:              randString(64, hexCharset),
		CommitmentAddress: randString(35, addrCharset),
		FeeAddressIndex:   12345,
		FeeAddress:        randString(35, addrCharset),
		FeeAmount:         10000000,
		FeeExpiration:     4,
		Confirmed:         false,
		VoteChoices:       map[string]string{"AgendaID": "Choice"},
		VotingWIF:         randString(53, addrCharset),
		FeeTxHex:          randString(504, hexCharset),
		FeeTxHash:         randString(64, hexCharset),
		FeeTxStatus:       database.FeeBroadcast,
		AltSigAddress:     randString(35, addrCharset),
	}
}

// Ensure that testNode satisfies Node.
var _ Node = (*testNode)(nil)

type testNode struct {
	canTicketVote        bool
	canTicketVoteErr     error
	getRawTransactionErr error
}

func (n *testNode) CanTicketVote(_ *dcrdtypes.TxRawResult, _ string, _ *chaincfg.Params) (bool, error) {
	return n.canTicketVote, n.canTicketVoteErr
}

func (n *testNode) GetRawTransaction(txHash string) (*dcrdtypes.TxRawResult, error) {
	return nil, n.getRawTransactionErr
}

func TestSetAltSig(t *testing.T) {
	const testAddr = "DsVoDXNQqyF3V83PJJ5zMdnB4pQuJHBAh15"
	tests := []struct {
		name                 string
		vspClosed, clearHash bool
		deformReq            int
		setMaxHistory        bool
		deformSig            string
		addr                 string
		getRawTransactionErr error
		canTicketNotVote     bool
		isNewTicket          bool
		canTicketVoteErr     error
		wantCode             int
	}{{
		name:     "ok",
		addr:     testAddr,
		wantCode: http.StatusOK,
	}, {
		name:        "ok new ticket",
		addr:        testAddr,
		isNewTicket: true,
		wantCode:    http.StatusOK,
	}, {
		name:     "ok no addr",
		wantCode: http.StatusOK,
	}, {
		name:          "ok no addr with history at max",
		setMaxHistory: true,
		wantCode:      http.StatusOK,
	}, {
		name:      "vsp closed",
		vspClosed: true,
		wantCode:  http.StatusBadRequest,
	}, {
		name:      "bad request",
		deformReq: 1,
		wantCode:  http.StatusBadRequest,
	}, {
		name:     "bad addr",
		addr:     "xxx",
		wantCode: http.StatusBadRequest,
	}, {
		name:     "addr wrong type",
		addr:     "DkM3ZigNyiwHrsXRjkDQ8t8tW6uKGW9g61qEkG3bMqQPQWYEf5X3J",
		wantCode: http.StatusBadRequest,
	}, {
		name:                 "error getting raw tx from dcrd client",
		getRawTransactionErr: errors.New("get raw transaction error"),
		wantCode:             http.StatusInternalServerError,
	}, {
		name:             "error getting can vote from dcrd client",
		canTicketVoteErr: errors.New("can ticket vote error"),
		wantCode:         http.StatusInternalServerError,
	}, {
		name:             "ticket can't vote",
		canTicketNotVote: true,
		wantCode:         http.StatusBadRequest,
	}, {
		name:      "bad sig",
		addr:      testAddr,
		deformSig: "x",
		wantCode:  http.StatusInternalServerError,
	}, {
		name:          "hist at max",
		addr:          testAddr,
		setMaxHistory: true,
		wantCode:      http.StatusBadRequest,
	}, {
		name:        "new ticket nothing to set",
		isNewTicket: true,
		wantCode:    http.StatusBadRequest,
	}}

	for _, test := range tests {
		tTicket := exampleTicket()
		if !test.isNewTicket {
			if err := db.InsertNewTicket(tTicket); err != nil {
				t.Fatalf("%q: unable to insert ticket: %v", test.name, err)
			}
		}

		cfg.VspClosed = test.vspClosed

		tNode := &testNode{
			canTicketVote:        !test.canTicketNotVote,
			canTicketVoteErr:     test.canTicketVoteErr,
			getRawTransactionErr: test.getRawTransactionErr,
		}
		var w *httptest.ResponseRecorder
		request := func() {
			w = httptest.NewRecorder()
			c, r := gin.CreateTestContext(w)

			req := &SetAltSigRequest{
				Timestamp:     time.Now().Unix(),
				TicketHash:    tTicket.Hash,
				TicketHex:     randString(504, hexCharset),
				ParentHex:     randString(504, hexCharset),
				AltPubAddress: test.addr,
			}
			b, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}

			handle := func(c *gin.Context) {
				c.Set("Ticket", tTicket)
				c.Set("KnownTicket", !test.isNewTicket)
				c.Set("CommitmentAddress", tTicket.Hash)
				c.Set("SigningAddress", tTicket.AltSigAddress)
				c.Set("DcrdClient", tNode)
				c.Set("RequestBytes", b[test.deformReq:])
				setAltSig(c)
			}

			r.POST("/", handle)

			c.Request, err = http.NewRequest(http.MethodPost, "/", nil)
			if err != nil {
				t.Fatal(err)
			}

			reqSig := randString(504, hexCharset)
			c.Request.Header.Set("VSP-Client-Signature", reqSig+test.deformSig)

			r.ServeHTTP(w, c.Request)
		}

		// Max history is currently one.
		if test.setMaxHistory {
			for i := 0; i < 1; i++ {
				request()
			}
		}
		request()

		if test.wantCode != w.Code {
			t.Errorf("%q: expected status %d, got %d", test.name, test.wantCode, w.Code)
		}
	}
}
