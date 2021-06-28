// Copyright (c) 2020-2021 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package webapi

import (
	"bytes"
	"errors"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/decred/dcrd/blockchain/stake/v4"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/vspd/rpc"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/gorilla/sessions"
	"github.com/jrick/wsrpc/v2"
)

// withSession middleware adds a gorilla session to the request context for
// downstream handlers to make use of. Sessions are used by admin pages to
// maintain authentication status.
func withSession(store *sessions.CookieStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := store.Get(c.Request, "vspd-session")
		if err != nil {
			// "value is not valid" occurs if the cookie secret changes. This is
			// common during development (eg. when using the test harness) but
			// it should not occur in production.
			if strings.Contains(err.Error(), "securecookie: the value is not valid") {
				log.Warn("Cookie secret has changed. Generating new session.")

				// Persist the newly generated session.
				err = store.Save(c.Request, c.Writer, session)
				if err != nil {
					log.Errorf("Error saving session: %v", err)
					c.String(http.StatusInternalServerError, "Error saving session")
					c.Abort()
					return
				}
			} else {
				log.Errorf("Session error: %v", err)
				c.String(http.StatusInternalServerError, "Error getting session")
				c.Abort()
				return
			}
		}

		c.Set("session", session)
	}
}

// requireAdmin will only allow the request to proceed if the current session is
// authenticated as an admin, otherwise it will render the login template.
func requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := c.MustGet("session").(*sessions.Session)
		admin := session.Values["admin"]

		if admin == nil {
			c.HTML(http.StatusUnauthorized, "login.html", gin.H{
				"WebApiCache": getCache(),
				"WebApiCfg":   cfg,
			})
			c.Abort()
			return
		}
	}
}

// withDcrdClient middleware adds a dcrd client to the request context for
// downstream handlers to make use of.
func withDcrdClient(dcrd rpc.DcrdConnect) gin.HandlerFunc {
	return func(c *gin.Context) {
		client, err := dcrd.Client(c, cfg.NetParams)
		if err != nil {
			log.Error(err)
			sendError(errInternalError, c)
			return
		}

		c.Set("DcrdClient", client)
	}
}

// withWalletClients middleware attempts to add voting wallet clients to the
// request context for downstream handlers to make use of. Downstream handlers
// must handle the case where no wallet clients are connected.
func withWalletClients(wallets rpc.WalletConnect) gin.HandlerFunc {
	return func(c *gin.Context) {
		clients, failedConnections := wallets.Clients(c, cfg.NetParams)
		if len(clients) == 0 {
			log.Error("Could not connect to any wallets")
		} else if len(failedConnections) > 0 {
			log.Errorf("Failed to connect to %d wallet(s), proceeding with only %d",
				len(failedConnections), len(clients))
		}
		c.Set("WalletClients", clients)
		c.Set("FailedWalletClients", failedConnections)
	}
}

// drainAndReplaceBody will read and return the body of the provided request. It
// replaces the request reader with an identical one so it can be used again.
func drainAndReplaceBody(req *http.Request) ([]byte, error) {
	reqBytes, err := ioutil.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body.Close()
	req.Body = ioutil.NopCloser(bytes.NewBuffer(reqBytes))
	return reqBytes, nil
}

// broadcastTicket will ensure that the local dcrd instance is aware of the
// provided ticket.
// Ticket hash, ticket hex, and parent hex are parsed from the request body and
// validated. They are broadcast to the network using SendRawTransaction if dcrd
// is not aware of them.
func broadcastTicket() gin.HandlerFunc {
	return func(c *gin.Context) {
		const funcName = "broadcastTicket"

		// Read request bytes.
		reqBytes, err := drainAndReplaceBody(c.Request)
		if err != nil {
			log.Warnf("%s: Error reading request (clientIP=%s): %v", funcName, c.ClientIP(), err)
			sendErrorWithMsg(err.Error(), errBadRequest, c)
			return
		}

		// Parse request to ensure ticket hash/hex and parent hex are included.
		var request struct {
			TicketHex  string `json:"tickethex" binding:"required"`
			TicketHash string `json:"tickethash" binding:"required"`
			ParentHex  string `json:"parenthex" binding:"required"`
		}
		if err := binding.JSON.BindBody(reqBytes, &request); err != nil {
			log.Warnf("%s: Bad request (clientIP=%s): %v", funcName, c.ClientIP(), err)
			sendErrorWithMsg(err.Error(), errBadRequest, c)
			return
		}

		// Ensure the provided ticket hex is a valid ticket.
		msgTx, err := decodeTransaction(request.TicketHex)
		if err != nil {
			log.Errorf("%s: Failed to decode ticket hex (ticketHash=%s): %v", funcName, request.TicketHash, err)
			sendErrorWithMsg("cannot decode ticket hex", errBadRequest, c)
			return
		}

		err = isValidTicket(msgTx)
		if err != nil {
			log.Warnf("%s: Invalid ticket (clientIP=%s, ticketHash=%s): %v",
				funcName, c.ClientIP(), request.TicketHash, err)
			sendError(errInvalidTicket, c)
			return
		}

		// Ensure hex matches hash.
		if msgTx.TxHash().String() != request.TicketHash {
			log.Warnf("%s: Ticket hex/hash mismatch (clientIP=%s, ticketHash=%s)",
				funcName, c.ClientIP(), request.TicketHash)
			sendErrorWithMsg("ticket hex does not match hash", errBadRequest, c)
			return
		}

		// Ensure the provided parent hex is a valid tx.
		parentTx, err := decodeTransaction(request.ParentHex)
		if err != nil {
			log.Errorf("%s: Failed to decode parent hex (ticketHash=%s): %v", funcName, request.TicketHash, err)
			sendErrorWithMsg("cannot decode parent hex", errBadRequest, c)
			return
		}
		parentHash := parentTx.TxHash()

		// Check if local dcrd already knows the parent tx.
		dcrdClient := c.MustGet("DcrdClient").(*rpc.DcrdRPC)
		_, err = dcrdClient.GetRawTransaction(parentHash.String())
		var e *wsrpc.Error
		if err == nil {
			// No error means dcrd already knows the parent tx, nothing to do.
		} else if errors.As(err, &e) && e.Code == rpc.ErrNoTxInfo {
			// ErrNoTxInfo means local dcrd is not aware of the parent. We have
			// the hex, so we can broadcast it here.

			// Before broadcasting, check that the provided parent hex is
			// actually the parent of the ticket.
			var found bool
			for _, txIn := range msgTx.TxIn {
				if !txIn.PreviousOutPoint.Hash.IsEqual(&parentHash) {
					continue
				}
				found = true
				break
			}

			if !found {
				log.Errorf("%s: Invalid ticket parent (ticketHash=%s)", funcName, request.TicketHash)
				sendErrorWithMsg("invalid ticket parent", errBadRequest, c)
				return
			}

			log.Debugf("%s: Broadcasting parent tx %s (ticketHash=%s)", funcName, parentHash, request.TicketHash)
			err = dcrdClient.SendRawTransaction(request.ParentHex)
			if err != nil {
				log.Errorf("%s: dcrd.SendRawTransaction for parent tx failed (ticketHash=%s): %v",
					funcName, request.TicketHash, err)
				sendError(errCannotBroadcastTicket, c)
				return
			}

		} else {
			log.Errorf("%s: dcrd.GetRawTransaction for ticket parent failed (ticketHash=%s): %v",
				funcName, request.TicketHash, err)
			sendError(errInternalError, c)
			return
		}

		// Check if local dcrd already knows the ticket.
		_, err = dcrdClient.GetRawTransaction(request.TicketHash)
		if err == nil {
			// No error means dcrd already knows the ticket, we are done here.
			return
		}

		// ErrNoTxInfo means local dcrd is not aware of the ticket. We have the
		// hex, so we can broadcast it here.
		if errors.As(err, &e) && e.Code == rpc.ErrNoTxInfo {
			log.Debugf("%s: Broadcasting ticket (ticketHash=%s)", funcName, request.TicketHash)
			err = dcrdClient.SendRawTransaction(request.TicketHex)
			if err != nil {
				log.Errorf("%s: dcrd.SendRawTransaction for ticket failed (ticketHash=%s): %v",
					funcName, request.TicketHash, err)
				sendError(errCannotBroadcastTicket, c)
				return
			}
		} else {
			log.Errorf("%s: dcrd.GetRawTransaction for ticket failed (ticketHash=%s): %v",
				funcName, request.TicketHash, err)
			sendError(errInternalError, c)
			return
		}
	}
}

// vspAuth middleware reads the request body and extracts the ticket hash. The
// commitment address for the ticket is retrieved from the database if it is
// known, or it is retrieved from the chain if not.
// The middleware errors out if the VSP-Client-Signature header of the request
// does not contain the request body signed with the commitment address.
// Ticket information is added to the request context for downstream handlers to
// use.
func vspAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		const funcName = "vspAuth"

		// Read request bytes.
		reqBytes, err := drainAndReplaceBody(c.Request)
		if err != nil {
			log.Warnf("%s: Error reading request (clientIP=%s): %v", funcName, c.ClientIP(), err)
			sendErrorWithMsg(err.Error(), errBadRequest, c)
			return
		}

		// Add request bytes to request context for downstream handlers to reuse.
		// Necessary because the request body reader can only be used once.
		c.Set("RequestBytes", reqBytes)

		// Parse request and ensure there is a ticket hash included.
		var request struct {
			TicketHash string `json:"tickethash" binding:"required"`
		}
		if err := binding.JSON.BindBody(reqBytes, &request); err != nil {
			log.Warnf("%s: Bad request (clientIP=%s): %v", funcName, c.ClientIP(), err)
			sendErrorWithMsg(err.Error(), errBadRequest, c)
			return
		}
		hash := request.TicketHash

		// Before hitting the db or any RPC, ensure this is a valid ticket hash.
		// A ticket hash should be 64 chars (MaxHashStringSize) and should parse
		// into a chainhash.Hash without error.
		if len(hash) != chainhash.MaxHashStringSize {
			log.Errorf("%s: Incorrect hash length (clientIP=%s): got %d, expected %d",
				funcName, c.ClientIP(), len(hash), chainhash.MaxHashStringSize)
			sendErrorWithMsg("invalid ticket hash", errBadRequest, c)
			return
		}
		_, err = chainhash.NewHashFromStr(hash)
		if err != nil {
			log.Errorf("%s: Invalid hash (clientIP=%s): %v", funcName, c.ClientIP(), err)
			sendErrorWithMsg("invalid ticket hash", errBadRequest, c)
			return
		}

		// Check if this ticket already appears in the database.
		ticket, ticketFound, err := db.GetTicketByHash(hash)
		if err != nil {
			log.Errorf("%s: db.GetTicketByHash error (ticketHash=%s): %v", funcName, hash, err)
			sendError(errInternalError, c)
			return
		}

		// validate validates the signature in the request as signing
		// the request body with private key corresponding to sAddr.
		// fatal will cause the client to be notified of failure if
		// verification fails. Returns wether verification succeeded and
		// sets some header values if success.
		validate := func(cAddr, sAddr string, kt, fatal bool) bool {
			// Validate request signature to ensure ticket ownership.
			err = validateSignature(reqBytes, sAddr, c)
			if err != nil {
				log.Warnf("%s: Bad signature (clientIP=%s, ticketHash=%s): %v", funcName, c.ClientIP(), hash, err)
				if fatal {
					sendError(errBadSignature, c)
				}
				return false
			}
			log.Tracef("%s: Authed user signature (clientIP=%s, ticketHash=%s)", funcName, c.ClientIP(), hash)

			// Add ticket information to context so downstream handlers don't need
			// to access the db for it.
			c.Set("Ticket", ticket)
			c.Set("KnownTicket", kt)
			c.Set("CommitmentAddress", cAddr)
			c.Set("SigningAddress", sAddr)
			return true
		}

		// If the ticket was found in the database, we already know its
		// commitment address. Otherwise we need to get it from the chain.
		// The signingAddress is the alternate signature address if
		// present for the ticket. Otherwise the commitment address is used.
		if ticketFound {
			// If an alternate signing address is set, try it.
			// Fall back to the commitment address.
			if ticket.AltSigAddress != "" && validate(ticket.AltSigAddress, ticket.CommitmentAddress, true, false) {
				return
			}
			_ = validate(ticket.CommitmentAddress, ticket.CommitmentAddress, true, true)
			return
		}
		dcrdClient := c.MustGet("DcrdClient").(*rpc.DcrdRPC)

		resp, err := dcrdClient.GetRawTransaction(hash)
		if err != nil {
			log.Errorf("%s: dcrd.GetRawTransaction for ticket failed (ticketHash=%s): %v", funcName, hash, err)
			sendError(errInternalError, c)
			return
		}

		msgTx, err := decodeTransaction(resp.Hex)
		if err != nil {
			log.Errorf("%s: Failed to decode ticket hex (ticketHash=%s): %v", funcName, ticket.Hash, err)
			sendError(errInternalError, c)
			return
		}

		err = isValidTicket(msgTx)
		if err != nil {
			log.Warnf("%s: Invalid ticket (clientIP=%s, ticketHash=%s): %v", funcName, c.ClientIP(), hash, err)
			sendError(errInvalidTicket, c)
			return
		}

		addr, err := stake.AddrFromSStxPkScrCommitment(msgTx.TxOut[1].PkScript, cfg.NetParams)
		if err != nil {
			log.Errorf("%s: AddrFromSStxPkScrCommitment error (ticketHash=%s): %v", funcName, hash, err)
			sendError(errInternalError, c)
			return
		}

		commAddr := addr.String()
		_ = validate(commAddr, commAddr, false, true)
	}

}
