// Copyright (c) 2020-2021 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package database

import (
	"context"
	"crypto/ed25519"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

const (
	testDb               = "test.db"
	backupDb             = "test.db-backup"
	feeXPub              = "feexpub"
	maxVoteChangeRecords = 3
)

var (
	db *VspDatabase
)

// TestDatabase runs all database tests.
func TestDatabase(t *testing.T) {
	// Ensure we are starting with a clean environment.
	os.Remove(testDb)
	os.Remove(backupDb)

	// All sub-tests to run.
	tests := map[string]func(*testing.T){
		"testCreateNew":         testCreateNew,
		"testInsertNewTicket":   testInsertNewTicket,
		"testGetTicketByHash":   testGetTicketByHash,
		"testUpdateTicket":      testUpdateTicket,
		"testTicketFeeExpired":  testTicketFeeExpired,
		"testFilterTickets":     testFilterTickets,
		"testCountTickets":      testCountTickets,
		"testAddressIndex":      testAddressIndex,
		"testDeleteTicket":      testDeleteTicket,
		"testVoteChangeRecords": testVoteChangeRecords,
		"testHTTPBackup":        testHTTPBackup,
		"testAddAltSigHistory":  testAddAltSigHistory,
		"testAltSigHistory":     testAltSigHistory,
	}

	for testName, test := range tests {
		// Create a new blank database for each sub-test.
		var err error
		var wg sync.WaitGroup
		ctx, cancel := context.WithCancel(context.TODO())
		err = CreateNew(testDb, feeXPub)
		if err != nil {
			t.Fatalf("error creating test database: %v", err)
		}
		db, err = Open(ctx, &wg, testDb, time.Hour, maxVoteChangeRecords)
		if err != nil {
			t.Fatalf("error opening test database: %v", err)
		}

		// Run the sub-test.
		t.Run(testName, test)

		// Request database shutdown and wait for it to complete.
		cancel()
		wg.Wait()

		db.Close()

		os.Remove(testDb)
		os.Remove(backupDb)
	}
}

func testCreateNew(t *testing.T) {
	// A newly created DB should contain a signing keypair.
	priv, pub, err := db.KeyPair()
	if err != nil {
		t.Fatalf("error getting keypair: %v", err)
	}

	// Ensure keypair can be used for signing/verifying messages.
	msg := []byte("msg")
	sig := ed25519.Sign(priv, msg)
	if !ed25519.Verify(pub, msg, sig) {
		t.Fatalf("keypair from database could not be used to sign/verify a message")
	}

	// A newly created DB should have a cookie secret.
	secret, err := db.CookieSecret()
	if err != nil {
		t.Fatalf("error getting cookie secret: %v", err)
	}

	if len(secret) != 32 {
		t.Fatalf("expected a 32 byte cookie secret, got %d bytes", len(secret))
	}

	// A newly created DB should store the fee xpub it was initialized with.
	retrievedXPub, err := db.FeeXPub()
	if err != nil {
		t.Fatalf("error getting fee xpub: %v", err)
	}

	if retrievedXPub != feeXPub {
		t.Fatalf("expected fee xpub %v, got %v", feeXPub, retrievedXPub)
	}
}

func testHTTPBackup(t *testing.T) {
	// Capture the HTTP response written by the backup func.
	rr := httptest.NewRecorder()
	err := db.BackupDB(rr)
	if err != nil {
		t.Fatal(err)
	}

	// Check the HTTP status is OK.
	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("wrong HTTP status code: expected %v, got %v",
			http.StatusOK, status)
	}

	// Check HTTP headers.
	header := "Content-Type"
	expected := "application/octet-stream"
	if actual := rr.Header().Get(header); actual != expected {
		t.Errorf("wrong %s header: expected %s, got %s",
			header, expected, actual)
	}

	header = "Content-Disposition"
	expected = `attachment; filename="vspd.db"`
	if actual := rr.Header().Get(header); actual != expected {
		t.Errorf("wrong %s header: expected %s, got %s",
			header, expected, actual)
	}

	header = "Content-Length"
	cLength, err := strconv.Atoi(rr.Header().Get(header))
	if err != nil {
		t.Fatalf("could not convert %s to integer: %v", header, err)
	}

	if cLength <= 0 {
		t.Fatalf("expected a %s greater than zero, got %d", header, cLength)
	}

	// Check reported length matches actual.
	body, err := ioutil.ReadAll(rr.Result().Body)
	if err != nil {
		t.Fatalf("could not read http response body: %v", err)
	}

	if len(body) != cLength {
		t.Fatalf("expected reported content-length to match actual body length. %v != %v",
			cLength, len(body))
	}
}
