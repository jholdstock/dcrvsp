# API

## General notes

- Success responses use HTTP status 200 and a JSON encoded body.

- Error responses use HTTP status 500 to indicate a server error or 4XX to
  indicate a client error, and will include a JSON body describing the error.
  For example:

  ```json
  {"code": 9, "message":"invalid vote choices"}
  ```

  A full list of error codes can be looked up in
  [webapi/errors.go](../webapi/errors.go)

- Requests which reference specific tickets need to be properly signed as
  described in [two-way-accountability.md](./two-way-accountability.md).

- Implementation of request and response types can be found in
  [webapi/types.go](../webapi/types.go).

- The initial version of the vspd API is version 3. This is because the first
  version of the vspd API actually represents the third iteration of VSP APIs.
  The first and second iterations of VSP API were implemented by
  [dcrstakepool](https://github.com/decred/dcrstakepool).

## Expected usage

### Get VSP info

Clients should retrieve the VSP's public key so they can check the signature on
future API responses. A VSP should never change their public key, so it can be
requested once and cached indefinitely. `vspclosed` indicates that the VSP is
not currently accepting new tickets. Calling `/feeaddress` or `/payfee`
when a VSP is closed will result in an error.

- `GET /api/v3/vspinfo`

    No request body.

    Response:

    ```json
    {
        "apiversions":[3],
        "timestamp":1590599436,
        "pubkey":"SjAmrAqH7LScCUwM1qo5O6Cu7aKhrM1ORszgZwD7HmU=",
        "feepercentage":3.0,
        "vspclosed":false,
        "vspclosedmsg": "",
        "network":"testnet3",
        "vspdversion":"1.0.0-pre",
        "voting":10,
        "voted":25,
        "revoked":3,
        "blockheight":623212,
        "estimatednetworkproportion":0.048478414
    }
    ```

### Register ticket

**Registering a ticket is a two step process. The VSP will not add a ticket to
its voting wallets unless both of these calls have succeeded.**

#### Step Zero (optional)

Set an alternate signing address for a ticket. The ticket must be valid and will
be transmitted to the network if not found. If set, the vsp will check that a
signature is good for this address and fallback to the commitment address if not.
The address must be valid for the network and a pay to secp256k1 ecdsa pubkey
hash script. Can be cleared by sending `""` as the `altpubaddress`. The key may
be set a maximum of three times. Further requests to set a new key will be
rejected, but the key may still be cleared.

- `POST /api/v3/setaltsig`

    Request:

    ```json
    {
        "timestamp":1590509066,
        "tickethash":"1b9f5dc3b4872c47f66b148b0633647458123d72a0f0623a90890cc51a668737",
        "tickethex":"0100000001a8...bfa6e4bf9c5ec1",
        "parenthex":"0100000022a7...580771a3064710",
        "altpubaddress":"Tsfkn6k9AoYgVZRV6ZzcgmuVSgCdJQt9JY2"
    }

    ```

    Response:

    ```json
    {
        "timestamp":1590509066,
        "request": {"<Copy of request body>"}
    }
    ```

#### Step One

Request fee amount and address for a ticket. The fee amount is only valid until
the expiration time has passed. The fee amount is an absolute value measured in
DCR. Returns an error if the specified ticket is not currently immature or live.

This call will return an error if a fee transaction has already been provided
for the specified ticket.

- `POST /api/v3/feeaddress`

    Request:

    ```json
    {
        "timestamp":1590509066,
        "tickethash":"1b9f5dc3b4872c47f66b148b0633647458123d72a0f0623a90890cc51a668737",
        "tickethex":"0100000001a8...bfa6e4bf9c5ec1",
        "parenthex":"0100000022a7...580771a3064710"
    }

    ```

    Response:

    ```json
    {
        "timestamp":1590509066,
        "feeaddress":"Tsfkn6k9AoYgVZRV6ZzcgmuVSgCdJQt9JY2",
        "feeamount":0.001,
        "expiration":1590563759,
        "request": {"<Copy of request body>"}
    }
    ```

#### Step Two

Provide the voting key for the ticket, voting preference, and a signed
transaction which pays the fee to the specified address. If the fee has expired,
this call will return an error and the client will need to request a new fee by
calling `/feeaddress` again. Returns an error if the specified ticket is not
currently immature or live.

The VSP will not broadcast the fee transaction until the ticket purchase has 6
confirmations. For this reason, it is important that the client ensures the
output being spent in the transaction is not spent elsewhere.

The VSP will not add the ticket to its voting wallets until the fee transaction
has 6 confirmations.

This call will return an error if a fee transaction has already been provided
for the specified ticket.

- `POST /api/v3/payfee`

    Request:

    ```json
    {
    "timestamp":1590509066,
    "tickethash":"484a68f7148e55d05f0b64a29fe7b148572cb5272d1ce2438cf15466d347f4f4",
    "feetx":"010000000125...737b266ffb9a93",
    "votingkey":"PtWUJWhSXsM9ztPkdtH8REe91z7uoidX8dsMChJUZ2spagm7YvrNm",
    "votechoices":{"headercommitments":"yes"}
    }
    ```

    Response:

    ```json
    {
    "timestamp":1590509066,
    "request": {"<Copy of request body>"}
    }
    ```

### Ticket Status

Clients can check the status of a ticket at any time after calling
`/feeaddress`.

- `ticketconfirmed` is true when the ticket purchase has 6 confirmations.
- `feetxstatus` can have the following values:
  - `none` - No fee transaction has been received yet.
  - `received` - Fee transaction has been received but not broadcast.
  - `broadcast` - Fee transaction has been broadcast but not confirmed.
  - `confirmed` - Fee transaction has been broadcast and confirmed.
  - `error` - Fee transaction could not be broadcast due to an error (eg. output
    in the tx was double spent).

If `feetxstatus` is `error`, the client needs to provide a new fee transaction
using `/payfee`. The VSP will only add a ticket to the voting wallets once
its `feetxstatus` is `confirmed`.

- `POST /api/v3/ticketstatus`

    Request:

    ```json
    {
        "tickethash":"484a68f7148e55d05f0b64a29fe7b148572cb5272d1ce2438cf15466d347f4f4"
    }
    ```

    Response:

    ```json
    {
      "timestamp":1590509066,
      "ticketconfirmed":true,
      "feetxstatus":"broadcast",
      "feetxhash": "e1c02b04b5bbdae66cf8e3c88366c4918d458a2d27a26144df37f54a2bc956ac",
      "votechoices":{"headercommitments":"no"},
      "request": {"<Copy of request body>"}
    }
    ```

### Update vote choices

Clients can update the voting preferences of their ticket at any time after
after calling `/payfee`.

- `POST /api/v3/setvotechoices`

    Request:

    ```json
    {
      "timestamp":1590509066,
      "tickethash":"484a68f7148e55d05f0b64a29fe7b148572cb5272d1ce2438cf15466d347f4f4",
      "votechoices":{"headercommitments":"no"}
    }
    ```

    Response:

    ```json
    {
      "timestamp":1590509066,
      "request": {"<Copy of request body>"}
    }
    ```
