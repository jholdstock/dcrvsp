# Two-way Accountability

In order to support two-way accountability, all vspd requests must be signed
with a private key corresponding to the relevant ticket, and all vspd responses
are signed by with a private key known only by the server.

## Client

### Client Request Signatures

Every client request which references a ticket should include a HTTP header
`VSP-Client-Signature`. The value of this header must be a signature of the
request body, signed with the commitment address of the referenced ticket, or
the alternate signature address if set.

### Client Accountability Example

A misbehaving user may attempt to discredit a VSP operator by falsely claiming
that the VSP did not vote a ticket according to the voting preferences selected
by the user.

In this case, the VSP operator can reveal the request signed by the user which
set the voting preferences, and demonstrate that this matches the voting
preferences which were recorded on the blockchain. It would then be incumbent on
the user to provide a signed request/response pair with a later timestamp to
demonstrate that the operator is being dishonest.

## Server

### Server Response Signatures

When vspd is started for the first time, it generates a ed25519 keypair and
stores it in the database. This key is used to sign all API responses, and the
signature is included in the response header `VSP-Server-Signature`.

### Server Accountability Example

A misbehaving server may fail to vote several tickets for which a user has paid
valid fees.

In this case, the user can reveal responses signed by the server which
demonstrate that the server has acknowledged receipt of the fees, and all
information necessary to have voted the ticket. It would then be incumbent on
the server to explain why these tickets were not voted.
