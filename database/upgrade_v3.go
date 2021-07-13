// Copyright (c) 2021 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package database

import (
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

func ticketBucketUpgrade(db *bolt.DB) error {
	log.Infof("Upgrading database to version %d", ticketBucketVersion)

	// Run the upgrade in a single database transaction so it can be safely
	// rolled back if an error is encountered.
	err := db.Update(func(tx *bolt.Tx) error {
		vspBkt := tx.Bucket(vspBktK)
		ticketBkt := vspBkt.Bucket(ticketBktK)

		// Count tickets so migration progress can be logged.
		todo := 0
		err := ticketBkt.ForEach(func(k, v []byte) error {
			todo++
			return nil
		})
		if err != nil {
			return fmt.Errorf("could not count tickets: %w", err)
		}

		done := 0
		const batchSize = 2000
		err = ticketBkt.ForEach(func(k, v []byte) error {
			// Deserialize the old ticket.
			var ticket v1Ticket
			err := json.Unmarshal(v, &ticket)
			if err != nil {
				return fmt.Errorf("could not unmarshal ticket: %w", err)
			}

			newTicket := Ticket{
				Hash:              ticket.Hash,
				PurchaseHeight:    ticket.PurchaseHeight,
				CommitmentAddress: ticket.CommitmentAddress,
				FeeAddressIndex:   ticket.FeeAddressIndex,
				FeeAddress:        ticket.FeeAddress,
				FeeAmount:         ticket.FeeAmount,
				FeeExpiration:     ticket.FeeExpiration,
				Confirmed:         ticket.Confirmed,
				VotingWIF:         ticket.VotingWIF,
				VoteChoices:       ticket.VoteChoices,
				FeeTxHex:          ticket.FeeTxHex,
				FeeTxHash:         ticket.FeeTxHash,
				FeeTxStatus:       ticket.FeeTxStatus,
				Outcome:           ticket.Outcome,
				// AltSigAddress was added later and must be
				// empty for a v1 Ticket. Added here for visibility.
				AltSigAddress: "",
			}

			// Delete the old ticket.
			err = ticketBkt.Delete(k)
			if err != nil {
				return fmt.Errorf("could not delete ticket: %w", err)
			}

			// Insert the new ticket.
			newBkt, err := ticketBkt.CreateBucket(k)
			if err != nil {
				return fmt.Errorf("could not create new ticket bucket: %w", err)
			}

			err = putTicketInBucket(newBkt, newTicket)
			if err != nil {
				return fmt.Errorf("could not put new ticket in bucket: %w", err)
			}

			done++

			if done%batchSize == 0 {
				log.Infof("Migrated %d/%d tickets", done, todo)
			}

			return nil
		})
		if err != nil {
			return err
		}

		if done > 0 && done%batchSize != 0 {
			log.Infof("Migrated %d/%d tickets", done, todo)
		}

		// Update database version.
		err = vspBkt.Put(versionK, uint32ToBytes(ticketBucketVersion))
		if err != nil {
			return fmt.Errorf("failed to update db version: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	log.Info("Upgrade completed")
	return nil
}
