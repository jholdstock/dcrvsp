// Copyright (c) 2020-2021 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package webapi

import (
	"sync"
	"time"

	"decred.org/dcrwallet/v2/wallet/txrules"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/vspd/database"
	"github.com/decred/vspd/rpc"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

// addrMtx protects getNewFeeAddress.
var addrMtx sync.Mutex

// getNewFeeAddress gets a new address from the address generator, and updates
// the last used address index in the database. In order to maintain consistency
// between the internal counter of address generator and the database, this func
// uses a mutex to ensure it is not run concurrently.
func getNewFeeAddress(db *database.VspDatabase, addrGen *addressGenerator) (string, uint32, error) {
	addrMtx.Lock()
	defer addrMtx.Unlock()

	addr, idx, err := addrGen.NextAddress()
	if err != nil {
		return "", 0, err
	}

	err = db.SetLastAddressIndex(idx)
	if err != nil {
		return "", 0, err
	}

	return addr, idx, nil
}

func getCurrentFee(dcrdClient *rpc.DcrdRPC) (dcrutil.Amount, error) {
	bestBlock, err := dcrdClient.GetBestBlockHeader()
	if err != nil {
		return 0, err
	}
	sDiff, err := dcrutil.NewAmount(bestBlock.SBits)
	if err != nil {
		return 0, err
	}

	// Using a hard-coded amount for relay fee is acceptable here because this
	// amount is never actually used to construct or broadcast transactions. It
	// is only used to calculate the fee charged for adding a ticket to the VSP.
	relayFee, err := dcrutil.NewAmount(0.0001)
	if err != nil {
		return 0, err
	}

	fee := txrules.StakePoolTicketFee(sDiff, relayFee, int32(bestBlock.Height),
		cfg.VSPFee, cfg.NetParams)
	if err != nil {
		return 0, err
	}
	return fee, nil
}

// feeAddress is the handler for "POST /api/v3/feeaddress".
func feeAddress(c *gin.Context) {

	const funcName = "feeAddress"

	// Get values which have been added to context by middleware.
	ticket := c.MustGet("Ticket").(database.Ticket)
	knownTicket := c.MustGet("KnownTicket").(bool)
	commitmentAddress := c.MustGet("CommitmentAddress").(string)
	dcrdClient := c.MustGet("DcrdClient").(*rpc.DcrdRPC)
	reqBytes := c.MustGet("RequestBytes").([]byte)

	if cfg.VspClosed {
		sendError(errVspClosed, c)
		return
	}

	var request feeAddressRequest
	if err := binding.JSON.BindBody(reqBytes, &request); err != nil {
		log.Warnf("%s: Bad request (clientIP=%s): %v", funcName, c.ClientIP(), err)
		sendErrorWithMsg(err.Error(), errBadRequest, c)
		return
	}

	ticketHash := request.TicketHash

	// Respond early if we already have the fee tx for this ticket.
	if knownTicket &&
		(ticket.FeeTxStatus == database.FeeReceieved ||
			ticket.FeeTxStatus == database.FeeBroadcast ||
			ticket.FeeTxStatus == database.FeeConfirmed) {
		log.Warnf("%s: Fee tx already received (clientIP=%s, ticketHash=%s)",
			funcName, c.ClientIP(), ticket.Hash)
		sendError(errFeeAlreadyReceived, c)
		return
	}

	// Get ticket details.
	rawTicket, err := dcrdClient.GetRawTransaction(ticketHash)
	if err != nil {
		log.Errorf("%s: dcrd.GetRawTransaction for ticket failed (ticketHash=%s): %v", funcName, ticketHash, err)
		sendError(errInternalError, c)
		return
	}

	// Ensure this ticket is eligible to vote at some point in the future.
	canVote, err := dcrdClient.CanTicketVote(rawTicket, ticketHash, cfg.NetParams)
	if err != nil {
		log.Errorf("%s: dcrd.CanTicketVote error (ticketHash=%s): %v", funcName, ticketHash, err)
		sendError(errInternalError, c)
		return
	}
	if !canVote {
		log.Warnf("%s: Unvotable ticket (clientIP=%s, ticketHash=%s)",
			funcName, c.ClientIP(), ticketHash)
		sendError(errTicketCannotVote, c)
		return
	}

	hasFeeAddr := ticket.FeeTxStatus != database.NoFeeAddress

	// VSP already knows this ticket and has already issued it a fee address.
	if knownTicket && hasFeeAddr {

		// If the expiry period has passed we need to issue a new fee.
		now := time.Now()
		if ticket.FeeExpired() {
			newFee, err := getCurrentFee(dcrdClient)
			if err != nil {
				log.Errorf("%s: getCurrentFee error (ticketHash=%s): %v", funcName, ticket.Hash, err)
				sendError(errInternalError, c)
				return
			}
			ticket.FeeExpiration = now.Add(feeAddressExpiration).Unix()
			ticket.FeeAmount = int64(newFee)

			err = db.UpdateTicket(ticket)
			if err != nil {
				log.Errorf("%s: db.UpdateTicket error, failed to update fee expiry (ticketHash=%s): %v",
					funcName, ticket.Hash, err)
				sendError(errInternalError, c)
				return
			}
			log.Debugf("%s: Expired fee updated (newFeeAmt=%s, ticketHash=%s)",
				funcName, newFee, ticket.Hash)
		}
		sendJSONResponse(feeAddressResponse{
			Timestamp:  now.Unix(),
			Request:    reqBytes,
			FeeAddress: ticket.FeeAddress,
			FeeAmount:  ticket.FeeAmount,
			Expiration: ticket.FeeExpiration,
		}, c)

		return
	}

	// Beyond this point we are processing a new ticket which the VSP has not
	// seen before or one that has no fee address.

	fee, err := getCurrentFee(dcrdClient)
	if err != nil {
		log.Errorf("%s: getCurrentFee error (ticketHash=%s): %v", funcName, ticketHash, err)
		sendError(errInternalError, c)
		return
	}

	newAddress, newAddressIdx, err := getNewFeeAddress(db, addrGen)
	if err != nil {
		log.Errorf("%s: getNewFeeAddress error (ticketHash=%s): %v", funcName, ticketHash, err)
		sendError(errInternalError, c)
		return
	}

	// Make sure this address is not in the database.
	has, err := db.HasFeeAddress(newAddress)
	if err != nil {
		log.Errorf("%s: db.HasFeeAddress error (ticketHash=%s): %v", funcName, ticketHash, err)
		sendError(errInternalError, c)
		return
	}
	if has {
		log.Errorf("%s: fee address exists in the database (ticketHash=%s newFeeAddress)", funcName, ticketHash, newAddress)
		sendError(errInternalError, c)
		return
	}

	now := time.Now()
	expire := now.Add(feeAddressExpiration).Unix()

	// Only set purchase height if the ticket already has 6 confs, otherwise its
	// purchase height may change due to reorgs.
	confirmed := false
	purchaseHeight := int64(0)
	if rawTicket.Confirmations >= requiredConfs {
		confirmed = true
		purchaseHeight = rawTicket.BlockHeight
	}

	existStatus := "new"
	if knownTicket {
		existStatus = "existing"
		ticket.FeeAddressIndex = newAddressIdx
		ticket.FeeAddress = newAddress
		ticket.FeeExpiration = now.Add(feeAddressExpiration).Unix()
		ticket.FeeAmount = int64(fee)
		ticket.Confirmed = confirmed
		ticket.PurchaseHeight = purchaseHeight
		err = db.UpdateTicket(ticket)
		if err != nil {
			log.Errorf("%s: db.UpdateTicket error, failed to update fee expiry (ticketHash=%s): %v",
				funcName, ticket.Hash, err)
			sendError(errInternalError, c)
			return
		}
	} else {
		dbTicket := database.Ticket{
			Hash:              ticketHash,
			PurchaseHeight:    purchaseHeight,
			CommitmentAddress: commitmentAddress,
			FeeAddressIndex:   newAddressIdx,
			FeeAddress:        newAddress,
			Confirmed:         confirmed,
			FeeAmount:         int64(fee),
			FeeExpiration:     expire,
			FeeTxStatus:       database.NoFee,
		}

		err = db.InsertNewTicket(dbTicket)
		if err != nil {
			log.Errorf("%s: db.InsertNewTicket failed (ticketHash=%s): %v", funcName, ticketHash, err)
			sendError(errInternalError, c)
			return
		}
	}

	log.Debugf("%s: Fee address created for %s ticket: (tktConfirmed=%t, feeAddrIdx=%d, "+
		"feeAddr=%s, feeAmt=%s, ticketHash=%s)",
		funcName, existStatus, confirmed, newAddressIdx, newAddress, fee, ticketHash)

	sendJSONResponse(feeAddressResponse{
		Timestamp:  now.Unix(),
		Request:    reqBytes,
		FeeAddress: newAddress,
		FeeAmount:  int64(fee),
		Expiration: expire,
	}, c)
}
