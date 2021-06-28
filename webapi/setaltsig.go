// Copyright (c) 2021 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package webapi

import (
	"encoding/hex"
	"errors"
	"time"

	"github.com/decred/dcrd/chaincfg/v3"
	dcrdtypes "github.com/decred/dcrd/rpc/jsonrpc/types/v3"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/vspd/database"
	"github.com/decred/vspd/rpc"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

// Ensure that Node is satisfied by *rpc.DcrdRPC.
var _ Node = (*rpc.DcrdRPC)(nil)

// Node is satisfied by *rpc.DcrdRPC and retrieves data from the blockchain.
type Node interface {
	CanTicketVote(rawTx *dcrdtypes.TxRawResult, ticketHash string, netParams *chaincfg.Params) (bool, error)
	GetRawTransaction(txHash string) (*dcrdtypes.TxRawResult, error)
}

// setAltSig is the handler for "POST /api/v3/setaltsig".
func setAltSig(c *gin.Context) {

	const funcName = "setAltSig"

	// Get values which have been added to context by middleware.
	ticket := c.MustGet("Ticket").(database.Ticket)
	knownTicket := c.MustGet("KnownTicket").(bool)
	commitmentAddress := c.MustGet("CommitmentAddress").(string)
	sigAddress := c.MustGet("SigningAddress").(string)
	dcrdClient := c.MustGet("DcrdClient").(Node)
	reqBytes := c.MustGet("RequestBytes").([]byte)

	if cfg.VspClosed {
		sendError(errVspClosed, c)
		return
	}

	var request SetAltSigRequest
	if err := binding.JSON.BindBody(reqBytes, &request); err != nil {
		log.Tracef("%s: Bad request (clientIP=%s): %v", funcName, c.ClientIP(), err)
		sendErrorWithMsg(err.Error(), errBadRequest, c)
		return
	}

	// Fail fast if the pubkey doesn't decode properly.
	if request.AltPubAddress != "" {
		addr, err := stdaddr.DecodeAddressV0(request.AltPubAddress, cfg.NetParams)
		if err != nil {
			log.Tracef("%s: Alt pub address cannot be decoded (clientIP=%s): %v", funcName, c.ClientIP(), err)
			sendErrorWithMsg(err.Error(), errBadRequest, c)
			return
		}
		if _, ok := addr.(*stdaddr.AddressPubKeyHashEcdsaSecp256k1V0); !ok {
			log.Tracef("%s: Alt pub address is unexpected type (clientIP=%s type=%T): %v", funcName, c.ClientIP(), addr, err)
			sendErrorWithMsg("wrong type for alternate signing address", errBadRequest, c)
			return
		}
	}

	ticketHash := request.TicketHash

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
		log.Tracef("%s: unvotable ticket (clientIP=%s, ticketHash=%s)",
			funcName, c.ClientIP(), ticketHash)
		sendError(errTicketCannotVote, c)
		return
	}

	addHistory := func() error {
		sigStr := c.GetHeader("VSP-Client-Signature")
		sig, err := hex.DecodeString(sigStr)
		if err != nil {
			log.Tracef("%s: error decoding signature string (ticketHash=%s, sig=%s): %v", funcName, ticketHash, sigStr, err)
			return err
		}
		history := &database.AltSigHistory{
			Addr: sigAddress,
			Req:  reqBytes,
			Sig:  sig,
		}
		err = db.AddAltSigHistory(ticketHash, history)
		if err != nil {
			log.Tracef("%s: db.AddAltSigHistory error, failed to set alt history (ticketHash=%s): %v",
				funcName, ticketHash, err)
			return err
		}
		return nil
	}

	// VSP already knows this ticket.
	if knownTicket {
		if request.AltPubAddress != "" {
			if err := addHistory(); err != nil {
				if errors.Is(err, database.ErrMaxAltSigs) {
					sendErrorWithMsg(err.Error(), errBadRequest, c)
					return
				}
				sendError(errInternalError, c)
				return
			}
		}
		ticket.AltSigAddress = request.AltPubAddress
		err = db.UpdateTicket(ticket)
		if err != nil {
			log.Errorf("%s: db.UpdateTicket error, failed to set alt signing pubkey (ticketHash=%s): %v",
				funcName, ticketHash, err)
			sendError(errInternalError, c)
			return
		}
		sendJSONResponse(SetAltSigResponse{
			Timestamp: time.Now().Unix(),
			Request:   reqBytes,
		}, c)
		return
	}

	// Beyond this point we are processing a new ticket which the VSP has not
	// seen before.

	// No need to allow set to defaults if new ticket.
	if request.AltPubAddress == "" {
		sendErrorWithMsg("no alternate address specified for new ticket", errBadRequest, c)
	}

	dbTicket := database.Ticket{
		Hash:              ticketHash,
		CommitmentAddress: commitmentAddress,
		FeeTxStatus:       database.NoFeeAddress,
		AltSigAddress:     request.AltPubAddress,
	}

	err = db.InsertNewTicket(dbTicket)
	if err != nil {
		log.Errorf("%s: db.InsertNewTicket failed (ticketHash=%s): %v", funcName, ticketHash, err)
		sendError(errInternalError, c)
		return
	}
	if err := addHistory(); err != nil {
		sendError(errInternalError, c)
		return
	}

	log.Tracef("%s: Alt sig pubkey set for new ticket: (ticketHash=%s)", funcName, ticketHash)

	sendJSONResponse(SetAltSigResponse{
		Timestamp: time.Now().Unix(),
		Request:   reqBytes,
	}, c)
}
