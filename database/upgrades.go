// Copyright (c) 2021 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package database

import (
	"fmt"

	bolt "go.etcd.io/bbolt"
)

const (
	// initialVersion is the version of a freshly created database which has had
	// no upgrades applied.
	initialVersion = 1

	// removeOldFeeTxVersion deletes any raw fee transactions which remain in
	// the database after already having been confirmed on-chain. There is no
	// need to keep these, and they take up a lot of space.
	removeOldFeeTxVersion = 2

	// ticketBucketVersion changes the way tickets are stored. Previously they
	// were stored as JSON encoded strings in a single bucket. This upgrade
	// moves each ticket into its own bucket and does away with JSON encoding.
	ticketBucketVersion = 3

	// latestVersion is the latest version of the database that is understood by
	// vspd. Databases with recorded versions higher than this will fail to open
	// (meaning any upgrades prevent reverting to older software).
	latestVersion = ticketBucketVersion
)

// upgrades maps between old database versions and the upgrade function to
// upgrade the database to the next version.
var upgrades = []func(tx *bolt.DB) error{
	initialVersion:        removeOldFeeTxUpgrade,
	removeOldFeeTxVersion: ticketBucketUpgrade,
}

// v1Ticket has the json tags required to unmarshal tickets stored in the
// v1 database format.
type v1Ticket struct {
	Hash              string            `json:"hsh"`
	PurchaseHeight    int64             `json:"phgt"`
	CommitmentAddress string            `json:"cmtaddr"`
	FeeAddressIndex   uint32            `json:"faddridx"`
	FeeAddress        string            `json:"faddr"`
	FeeAmount         int64             `json:"famt"`
	FeeExpiration     int64             `json:"fexp"`
	Confirmed         bool              `json:"conf"`
	VotingWIF         string            `json:"vwif"`
	VoteChoices       map[string]string `json:"vchces"`
	FeeTxHex          string            `json:"fhex"`
	FeeTxHash         string            `json:"fhsh"`
	FeeTxStatus       FeeStatus         `json:"fsts"`
	Outcome           TicketOutcome     `json:"otcme"`
}

// Upgrade will update the database to the latest known version.
func (vdb *VspDatabase) Upgrade(currentVersion uint32) error {
	if currentVersion == latestVersion {
		// No upgrades required.
		return nil
	}

	if currentVersion > latestVersion {
		// Database is too new.
		return fmt.Errorf("expected database version <= %d, got %d",
			latestVersion, currentVersion)
	}

	// Execute all necessary upgrades in order.
	for _, upgrade := range upgrades[currentVersion:] {
		err := upgrade(vdb.db)
		if err != nil {
			return err
		}
	}

	return nil
}
