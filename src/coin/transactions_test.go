package coin

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/skycoin/skycoin/src/cipher"
	"github.com/skycoin/skycoin/src/cipher/encoder"
	"github.com/skycoin/skycoin/src/testutil"
	_require "github.com/skycoin/skycoin/src/testutil/require"
)

func makeTransactionFromUxOut(t *testing.T, ux UxOut, s cipher.SecKey) Transaction {
	txn := Transaction{}
	txn.PushInput(ux.Hash())
	txn.PushOutput(makeAddress(), 1e6, 50)
	txn.PushOutput(makeAddress(), 5e6, 50)
	txn.SignInputs([]cipher.SecKey{s})
	err := txn.UpdateHeader()
	require.NoError(t, err)
	return txn
}

func makeTransaction(t *testing.T) Transaction {
	ux, s := makeUxOutWithSecret(t)
	return makeTransactionFromUxOut(t, ux, s)
}

func makeTransactions(t *testing.T, n int) Transactions { // nolint: unparam
	txns := make(Transactions, n)
	for i := range txns {
		txns[i] = makeTransaction(t)
	}
	return txns
}

func makeAddress() cipher.Address {
	p, _ := cipher.GenerateKeyPair()
	return cipher.AddressFromPubKey(p)
}

func copyTransaction(txn Transaction) Transaction {
	txo := Transaction{}
	txo.Length = txn.Length
	txo.Type = txn.Type
	txo.InnerHash = txn.InnerHash
	txo.Sigs = make([]cipher.Sig, len(txn.Sigs))
	copy(txo.Sigs, txn.Sigs)
	txo.In = make([]cipher.SHA256, len(txn.In))
	copy(txo.In, txn.In)
	txo.Out = make([]TransactionOutput, len(txn.Out))
	copy(txo.Out, txn.Out)
	return txo
}

func TestTransactionVerify(t *testing.T) {
	// Mismatch header hash
	txn := makeTransaction(t)
	txn.InnerHash = cipher.SHA256{}
	testutil.RequireError(t, txn.Verify(), "InnerHash does not match computed hash")

	// No inputs
	txn = makeTransaction(t)
	txn.In = make([]cipher.SHA256, 0)
	err := txn.UpdateHeader()
	require.NoError(t, err)
	testutil.RequireError(t, txn.Verify(), "No inputs")

	// No outputs
	txn = makeTransaction(t)
	txn.Out = make([]TransactionOutput, 0)
	err = txn.UpdateHeader()
	require.NoError(t, err)
	testutil.RequireError(t, txn.Verify(), "No outputs")

	// Invalid number of sigs
	txn = makeTransaction(t)
	txn.Sigs = make([]cipher.Sig, 0)
	err = txn.UpdateHeader()
	require.NoError(t, err)
	testutil.RequireError(t, txn.Verify(), "Invalid number of signatures")
	txn.Sigs = make([]cipher.Sig, 20)
	err = txn.UpdateHeader()
	require.NoError(t, err)
	testutil.RequireError(t, txn.Verify(), "Invalid number of signatures")

	// Too many sigs & inputs
	txn = makeTransaction(t)
	txn.Sigs = make([]cipher.Sig, math.MaxUint16)
	txn.In = make([]cipher.SHA256, math.MaxUint16)
	err = txn.UpdateHeader()
	require.NoError(t, err)
	testutil.RequireError(t, txn.Verify(), "Too many signatures and inputs")

	// Duplicate inputs
	ux, s := makeUxOutWithSecret(t)
	txn = makeTransactionFromUxOut(t, ux, s)
	txn.PushInput(txn.In[0])
	txn.Sigs = nil
	txn.SignInputs([]cipher.SecKey{s, s})
	err = txn.UpdateHeader()
	require.NoError(t, err)
	testutil.RequireError(t, txn.Verify(), "Duplicate spend")

	// Duplicate outputs
	txn = makeTransaction(t)
	to := txn.Out[0]
	txn.PushOutput(to.Address, to.Coins, to.Hours)
	err = txn.UpdateHeader()
	require.NoError(t, err)
	testutil.RequireError(t, txn.Verify(), "Duplicate output in transaction")

	// Invalid signature, empty
	txn = makeTransaction(t)
	txn.Sigs[0] = cipher.Sig{}
	testutil.RequireError(t, txn.Verify(), "Failed to recover pubkey from signature")
	// We can't check here for other invalid signatures:
	//      - Signatures signed by someone else, spending coins they don't own
	//      - Signature is for wrong hash
	// This must be done by blockchain tests, because we need the address
	// from the unspent being spent

	// Output coins are 0
	txn = makeTransaction(t)
	txn.Out[0].Coins = 0
	err = txn.UpdateHeader()
	require.NoError(t, err)
	testutil.RequireError(t, txn.Verify(), "Zero coin output")

	// Output coin overflow
	txn = makeTransaction(t)
	txn.Out[0].Coins = math.MaxUint64 - 3e6
	err = txn.UpdateHeader()
	require.NoError(t, err)
	testutil.RequireError(t, txn.Verify(), "Output coins overflow")

	// Output coins are not multiples of 1e6 (valid, decimal restriction is not enforced here)
	txn = makeTransaction(t)
	txn.Out[0].Coins += 10
	err = txn.UpdateHeader()
	require.NoError(t, err)
	txn.Sigs = nil
	txn.SignInputs([]cipher.SecKey{genSecret})
	require.NotEqual(t, txn.Out[0].Coins%1e6, uint64(0))
	require.NoError(t, txn.Verify())

	// Valid
	txn = makeTransaction(t)
	txn.Out[0].Coins = 10e6
	txn.Out[1].Coins = 1e6
	err = txn.UpdateHeader()
	require.NoError(t, err)
	require.Nil(t, txn.Verify())
}

func TestTransactionVerifyInput(t *testing.T) {
	// Invalid uxIn args
	txn := makeTransaction(t)
	_require.PanicsWithLogMessage(t, "txn.In != uxIn", func() {
		_ = txn.VerifyInputSignatures(nil) // nolint: errcheck
	})
	_require.PanicsWithLogMessage(t, "txn.In != uxIn", func() {
		_ = txn.VerifyInputSignatures(UxArray{}) // nolint: errcheck
	})
	_require.PanicsWithLogMessage(t, "txn.In != uxIn", func() {
		_ = txn.VerifyInputSignatures(make(UxArray, 3)) // nolint: errcheck
	})

	// txn.In != txn.Sigs
	ux, s := makeUxOutWithSecret(t)
	txn = makeTransactionFromUxOut(t, ux, s)
	txn.Sigs = []cipher.Sig{}
	_require.PanicsWithLogMessage(t, "txn.In != txn.Sigs", func() {
		_ = txn.VerifyInputSignatures(UxArray{ux}) // nolint: errcheck
	})

	ux, s = makeUxOutWithSecret(t)
	txn = makeTransactionFromUxOut(t, ux, s)
	txn.Sigs = append(txn.Sigs, cipher.Sig{})
	_require.PanicsWithLogMessage(t, "txn.In != txn.Sigs", func() {
		_ = txn.VerifyInputSignatures(UxArray{ux}) // nolint: errcheck
	})

	// txn.InnerHash != txn.HashInner()
	ux, s = makeUxOutWithSecret(t)
	txn = makeTransactionFromUxOut(t, ux, s)
	txn.InnerHash = cipher.SHA256{}
	_require.PanicsWithLogMessage(t, "Invalid Tx Inner Hash", func() {
		_ = txn.VerifyInputSignatures(UxArray{ux}) // nolint: errcheck
	})

	// txn.In does not match uxIn hashes
	ux, s = makeUxOutWithSecret(t)
	txn = makeTransactionFromUxOut(t, ux, s)
	_require.PanicsWithLogMessage(t, "Ux hash mismatch", func() {
		_ = txn.VerifyInputSignatures(UxArray{UxOut{}}) // nolint: errcheck
	})

	// Invalid signature
	ux, s = makeUxOutWithSecret(t)
	txn = makeTransactionFromUxOut(t, ux, s)
	txn.Sigs[0] = cipher.Sig{}
	err := txn.VerifyInputSignatures(UxArray{ux})
	testutil.RequireError(t, err, "Signature not valid for output being spent")

	// Valid
	ux, s = makeUxOutWithSecret(t)
	txn = makeTransactionFromUxOut(t, ux, s)
	err = txn.VerifyInputSignatures(UxArray{ux})
	require.NoError(t, err)
}

func TestTransactionPushInput(t *testing.T) {
	txn := &Transaction{}
	ux := makeUxOut(t)
	require.Equal(t, txn.PushInput(ux.Hash()), uint16(0))
	require.Equal(t, len(txn.In), 1)
	require.Equal(t, txn.In[0], ux.Hash())
	txn.In = append(txn.In, make([]cipher.SHA256, math.MaxUint16)...)
	ux = makeUxOut(t)
	require.Panics(t, func() { txn.PushInput(ux.Hash()) })
}

func TestTransactionPushOutput(t *testing.T) {
	txn := &Transaction{}
	a := makeAddress()
	txn.PushOutput(a, 100, 150)
	require.Equal(t, len(txn.Out), 1)
	require.Equal(t, txn.Out[0], TransactionOutput{
		Address: a,
		Coins:   100,
		Hours:   150,
	})
	for i := 1; i < 20; i++ {
		a := makeAddress()
		txn.PushOutput(a, uint64(i*100), uint64(i*50))
		require.Equal(t, len(txn.Out), i+1)
		require.Equal(t, txn.Out[i], TransactionOutput{
			Address: a,
			Coins:   uint64(i * 100),
			Hours:   uint64(i * 50),
		})
	}
}

func TestTransactionSignInputs(t *testing.T) {
	txn := &Transaction{}
	// Panics if txns already signed
	txn.Sigs = append(txn.Sigs, cipher.Sig{})
	require.Panics(t, func() { txn.SignInputs([]cipher.SecKey{}) })
	// Panics if not enough keys
	txn = &Transaction{}
	ux, s := makeUxOutWithSecret(t)
	txn.PushInput(ux.Hash())
	ux2, s2 := makeUxOutWithSecret(t)
	txn.PushInput(ux2.Hash())
	txn.PushOutput(makeAddress(), 40, 80)
	require.Equal(t, len(txn.Sigs), 0)
	require.Panics(t, func() { txn.SignInputs([]cipher.SecKey{s}) })
	require.Equal(t, len(txn.Sigs), 0)
	// Valid signing
	h := txn.HashInner()
	require.NotPanics(t, func() { txn.SignInputs([]cipher.SecKey{s, s2}) })
	require.Equal(t, len(txn.Sigs), 2)
	require.Equal(t, txn.HashInner(), h)
	p := cipher.MustPubKeyFromSecKey(s)
	a := cipher.AddressFromPubKey(p)
	p = cipher.MustPubKeyFromSecKey(s2)
	a2 := cipher.AddressFromPubKey(p)
	require.NoError(t, cipher.VerifyAddressSignedHash(a, txn.Sigs[0], cipher.AddSHA256(h, txn.In[0])))
	require.NoError(t, cipher.VerifyAddressSignedHash(a2, txn.Sigs[1], cipher.AddSHA256(h, txn.In[1])))
	require.Error(t, cipher.VerifyAddressSignedHash(a, txn.Sigs[1], h))
	require.Error(t, cipher.VerifyAddressSignedHash(a2, txn.Sigs[0], h))
}

func TestTransactionHash(t *testing.T) {
	txn := makeTransaction(t)
	require.NotEqual(t, txn.Hash(), cipher.SHA256{})
	require.NotEqual(t, txn.HashInner(), txn.Hash())
}

func TestTransactionUpdateHeader(t *testing.T) {
	txn := makeTransaction(t)
	h := txn.InnerHash
	txn.InnerHash = cipher.SHA256{}
	err := txn.UpdateHeader()
	require.NoError(t, err)
	require.NotEqual(t, txn.InnerHash, cipher.SHA256{})
	require.Equal(t, txn.InnerHash, h)
	require.Equal(t, txn.InnerHash, txn.HashInner())
}

func TestTransactionHashInner(t *testing.T) {
	txn := makeTransaction(t)

	h := txn.HashInner()
	require.NotEqual(t, h, cipher.SHA256{})

	// If txn.In is changed, hash should change
	tx2 := copyTransaction(txn)
	ux := makeUxOut(t)
	tx2.In[0] = ux.Hash()
	require.NotEqual(t, txn, tx2)
	require.Equal(t, tx2.In[0], ux.Hash())
	require.NotEqual(t, txn.HashInner(), tx2.HashInner())

	// If txn.Out is changed, hash should change
	tx2 = copyTransaction(txn)
	a := makeAddress()
	tx2.Out[0].Address = a
	require.NotEqual(t, txn, tx2)
	require.Equal(t, tx2.Out[0].Address, a)
	require.NotEqual(t, txn.HashInner(), tx2.HashInner())

	// If txn.Head is changed, hash should not change
	tx2 = copyTransaction(txn)
	txn.Sigs = append(txn.Sigs, cipher.Sig{})
	require.Equal(t, txn.HashInner(), tx2.HashInner())
}

func TestTransactionSerialization(t *testing.T) {
	txn := makeTransaction(t)
	b := txn.Serialize()
	tx2, err := TransactionDeserialize(b)
	require.NoError(t, err)
	require.Equal(t, txn, tx2)

	// Check reserializing deserialized txn
	b2 := tx2.Serialize()
	tx3, err := TransactionDeserialize(b2)
	require.NoError(t, err)
	require.Equal(t, tx2, tx3)

	// Check hex encode/decode followed by deserialize
	s := hex.EncodeToString(b)
	sb, err := hex.DecodeString(s)
	require.NoError(t, err)
	tx4, err := TransactionDeserialize(sb)
	require.NoError(t, err)
	require.Equal(t, tx2, tx4)

	// Invalid deserialization
	require.Panics(t, func() { MustTransactionDeserialize([]byte{0x04}) })
}

func TestTransactionOutputHours(t *testing.T) {
	txn := Transaction{}
	txn.PushOutput(makeAddress(), 1e6, 100)
	txn.PushOutput(makeAddress(), 1e6, 200)
	txn.PushOutput(makeAddress(), 1e6, 500)
	txn.PushOutput(makeAddress(), 1e6, 0)
	hours, err := txn.OutputHours()
	require.NoError(t, err)
	require.Equal(t, hours, uint64(800))

	txn.PushOutput(makeAddress(), 1e6, math.MaxUint64-700)
	_, err = txn.OutputHours()
	testutil.RequireError(t, err, "Transaction output hours overflow")
}

func TestTransactionsSize(t *testing.T) {
	txns := makeTransactions(t, 10)
	var size uint32
	for _, txn := range txns {
		encodedLen, err := IntToUint32(len(encoder.Serialize(&txn)))
		require.NoError(t, err)
		size, err = AddUint32(size, encodedLen)
		require.NoError(t, err)
	}

	require.NotEqual(t, size, 0)
	s, err := txns.Size()
	require.NoError(t, err)
	require.Equal(t, s, size)
}

func TestTransactionsHashes(t *testing.T) {
	txns := make(Transactions, 4)
	for i := 0; i < len(txns); i++ {
		txns[i] = makeTransaction(t)
	}
	hashes := txns.Hashes()
	require.Equal(t, len(hashes), 4)
	for i, h := range hashes {
		require.Equal(t, h, txns[i].Hash())
	}
}

func TestTransactionsTruncateBytesTo(t *testing.T) {
	txns := makeTransactions(t, 10)
	var trunc uint32
	for i := 0; i < len(txns)/2; i++ {
		size, err := txns[i].Size()
		require.NoError(t, err)
		trunc, err = AddUint32(trunc, size)
		require.NoError(t, err)
	}

	// Truncating halfway
	txns2, err := txns.TruncateBytesTo(trunc)
	require.NoError(t, err)
	require.Equal(t, len(txns2), len(txns)/2)
	totalSize, err := txns2.Size()
	require.NoError(t, err)
	require.Equal(t, totalSize, trunc)

	// Stepping into next boundary has same cutoff, must exceed
	trunc++
	txns2, err = txns.TruncateBytesTo(trunc)
	require.NoError(t, err)
	require.Equal(t, len(txns2), len(txns)/2)
	totalSize, err = txns2.Size()
	require.NoError(t, err)
	require.Equal(t, totalSize, trunc-1)

	// Moving to 1 before next level
	size5, err := txns[5].Size()
	require.NoError(t, err)
	require.True(t, size5 >= 2)
	trunc, err = AddUint32(trunc, size5-2)
	require.NoError(t, err)
	txns2, err = txns.TruncateBytesTo(trunc)
	require.NoError(t, err)
	require.Equal(t, len(txns2), len(txns)/2)

	totalSize, err = txns2.Size()
	require.NoError(t, err)
	size5, err = txns[5].Size()
	require.NoError(t, err)
	require.Equal(t, totalSize, trunc-size5+1)

	// Moving to next level
	trunc++
	txns2, err = txns.TruncateBytesTo(trunc)
	require.NoError(t, err)
	require.Equal(t, len(txns2), len(txns)/2+1)
	size, err := txns2.Size()
	require.NoError(t, err)
	require.Equal(t, size, trunc)

	// Truncating to full available amt
	trunc, err = txns.Size()
	require.NoError(t, err)
	txns2, err = txns.TruncateBytesTo(trunc)
	require.NoError(t, err)
	require.Equal(t, txns, txns2)
	size, err = txns2.Size()
	require.NoError(t, err)
	require.Equal(t, size, trunc)

	// Truncating over amount
	trunc++
	txns2, err = txns.TruncateBytesTo(trunc)
	require.NoError(t, err)
	require.Equal(t, txns, txns2)
	size, err = txns2.Size()
	require.NoError(t, err)
	require.Equal(t, size, trunc-1)

	// Truncating to 0
	trunc = 0
	txns2, err = txns.TruncateBytesTo(0)
	require.NoError(t, err)
	require.Equal(t, len(txns2), 0)
	size, err = txns2.Size()
	require.NoError(t, err)
	require.Equal(t, size, trunc)
}

func TestVerifyTransactionCoinsSpending(t *testing.T) {
	// Input coins overflow
	// Insufficient coins
	// Destroy coins

	type ux struct {
		coins uint64
		hours uint64
	}

	cases := []struct {
		name   string
		inUxs  []ux
		outUxs []ux
		err    error
	}{
		{
			name: "Input coins overflow",
			inUxs: []ux{
				{
					coins: math.MaxUint64 - 1e6 + 1,
					hours: 10,
				},
				{
					coins: 1e6,
					hours: 0,
				},
			},
			err: errors.New("Transaction input coins overflow"),
		},

		{
			name: "Output coins overflow",
			inUxs: []ux{
				{
					coins: 10e6,
					hours: 10,
				},
			},
			outUxs: []ux{
				{
					coins: math.MaxUint64 - 10e6 + 1,
					hours: 0,
				},
				{
					coins: 20e6,
					hours: 1,
				},
			},
			err: errors.New("Transaction output coins overflow"),
		},

		{
			name: "Insufficient coins",
			inUxs: []ux{
				{
					coins: 10e6,
					hours: 10,
				},
				{
					coins: 15e6,
					hours: 10,
				},
			},
			outUxs: []ux{
				{
					coins: 20e6,
					hours: 1,
				},
				{
					coins: 10e6,
					hours: 1,
				},
			},
			err: errors.New("Insufficient coins"),
		},

		{
			name: "Destroyed coins",
			inUxs: []ux{
				{
					coins: 10e6,
					hours: 10,
				},
				{
					coins: 15e6,
					hours: 10,
				},
			},
			outUxs: []ux{
				{
					coins: 5e6,
					hours: 1,
				},
				{
					coins: 10e6,
					hours: 1,
				},
			},
			err: errors.New("Transactions may not destroy coins"),
		},

		{
			name: "valid",
			inUxs: []ux{
				{
					coins: 10e6,
					hours: 10,
				},
				{
					coins: 15e6,
					hours: 10,
				},
			},
			outUxs: []ux{
				{
					coins: 10e6,
					hours: 11,
				},
				{
					coins: 10e6,
					hours: 1,
				},
				{
					coins: 5e6,
					hours: 0,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var uxIn, uxOut UxArray

			for _, ch := range tc.inUxs {
				uxIn = append(uxIn, UxOut{
					Body: UxBody{
						Coins: ch.coins,
						Hours: ch.hours,
					},
				})
			}

			for _, ch := range tc.outUxs {
				uxOut = append(uxOut, UxOut{
					Body: UxBody{
						Coins: ch.coins,
						Hours: ch.hours,
					},
				})
			}

			err := VerifyTransactionCoinsSpending(uxIn, uxOut)
			require.Equal(t, tc.err, err)
		})
	}
}

func TestVerifyTransactionHoursSpending(t *testing.T) {
	// Input hours overflow
	// Insufficient hours
	// NOTE: does not check for hours overflow, that had to be moved to soft constraints
	// NOTE: if uxIn.CoinHours() fails during the addition of earned hours to base hours,
	// the error is ignored and treated as 0 hours

	type ux struct {
		coins uint64
		hours uint64
	}

	cases := []struct {
		name     string
		inUxs    []ux
		outUxs   []ux
		headTime uint64
		err      string
	}{
		{
			name: "Input hours overflow",
			inUxs: []ux{
				{
					coins: 3e6,
					hours: math.MaxUint64 - 1e6 + 1,
				},
				{
					coins: 1e6,
					hours: 1e6,
				},
			},
			err: "Transaction input hours overflow",
		},

		{
			name: "Insufficient coin hours",
			inUxs: []ux{
				{
					coins: 10e6,
					hours: 10,
				},
				{
					coins: 15e6,
					hours: 10,
				},
			},
			outUxs: []ux{
				{
					coins: 15e6,
					hours: 10,
				},
				{
					coins: 10e6,
					hours: 11,
				},
			},
			err: "Insufficient coin hours",
		},

		{
			name: "coin hours time calculation overflow",
			inUxs: []ux{
				{
					coins: 10e6,
					hours: 10,
				},
				{
					coins: 15e6,
					hours: 10,
				},
			},
			outUxs: []ux{
				{
					coins: 10e6,
					hours: 11,
				},
				{
					coins: 10e6,
					hours: 1,
				},
				{
					coins: 5e6,
					hours: 0,
				},
			},
			headTime: math.MaxUint64,
			err:      "UxOut.CoinHours: Calculating whole coin seconds overflows uint64 seconds=18446744073709551615 coins=10 uxid=",
		},

		{
			name:     "Invalid (coin hours overflow when adding earned hours, which is treated as 0, and now enough coin hours)",
			headTime: 1e6,
			inUxs: []ux{
				{
					coins: 10e6,
					hours: math.MaxUint64,
				},
			},
			outUxs: []ux{
				{
					coins: 10e6,
					hours: 1,
				},
			},
			err: "Insufficient coin hours",
		},

		{
			name:     "Valid (coin hours overflow when adding earned hours, which is treated as 0, but not sending any hours)",
			headTime: 1e6,
			inUxs: []ux{
				{
					coins: 10e6,
					hours: math.MaxUint64,
				},
			},
			outUxs: []ux{
				{
					coins: 10e6,
					hours: 0,
				},
			},
		},

		{
			name: "Valid (base inputs have insufficient coin hours, but have sufficient after adjusting coinhours by headTime)",
			inUxs: []ux{
				{
					coins: 10e6,
					hours: 10,
				},
				{
					coins: 15e6,
					hours: 10,
				},
			},
			outUxs: []ux{
				{
					coins: 15e6,
					hours: 10,
				},
				{
					coins: 10e6,
					hours: 11,
				},
			},
			headTime: 1492707255,
		},

		{
			name: "valid",
			inUxs: []ux{
				{
					coins: 10e6,
					hours: 10,
				},
				{
					coins: 15e6,
					hours: 10,
				},
			},
			outUxs: []ux{
				{
					coins: 10e6,
					hours: 11,
				},
				{
					coins: 10e6,
					hours: 1,
				},
				{
					coins: 5e6,
					hours: 0,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var uxIn, uxOut UxArray

			for _, ch := range tc.inUxs {
				uxIn = append(uxIn, UxOut{
					Body: UxBody{
						Coins: ch.coins,
						Hours: ch.hours,
					},
				})
			}

			for _, ch := range tc.outUxs {
				uxOut = append(uxOut, UxOut{
					Body: UxBody{
						Coins: ch.coins,
						Hours: ch.hours,
					},
				})
			}

			err := VerifyTransactionHoursSpending(tc.headTime, uxIn, uxOut)
			if tc.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.True(t, strings.HasPrefix(err.Error(), tc.err))
			}
		})
	}
}

func TestTransactionsFees(t *testing.T) {
	calc := func(txn *Transaction) (uint64, error) {
		return 1, nil
	}

	var txns Transactions

	// Nil txns
	fee, err := txns.Fees(calc)
	require.NoError(t, err)
	require.Equal(t, uint64(0), fee)

	txns = append(txns, Transaction{})
	txns = append(txns, Transaction{})

	// 2 transactions, calc() always returns 1
	fee, err = txns.Fees(calc)
	require.NoError(t, err)
	require.Equal(t, uint64(2), fee)

	// calc error
	failingCalc := func(txn *Transaction) (uint64, error) {
		return 0, errors.New("bad calc")
	}
	_, err = txns.Fees(failingCalc)
	testutil.RequireError(t, err, "bad calc")

	// summing of calculated fees overflows
	overflowCalc := func(txn *Transaction) (uint64, error) {
		return math.MaxUint64, nil
	}

	_, err = txns.Fees(overflowCalc)
	testutil.RequireError(t, err, "Transactions fee totals overflow")
}

func TestSortTransactions(t *testing.T) {
	n := 6
	var txns Transactions
	for i := 0; i < n; i++ {
		txn := Transaction{}
		txn.PushOutput(makeAddress(), 1e6, uint64(i*1e3))
		err := txn.UpdateHeader()
		require.NoError(t, err)
		txns = append(txns, txn)
	}

	hashSortedTxns := append(Transactions{}, txns...)

	sort.Slice(hashSortedTxns, func(i, j int) bool {
		ihash := hashSortedTxns[i].Hash()
		jhash := hashSortedTxns[j].Hash()
		return bytes.Compare(ihash[:], jhash[:]) < 0
	})

	cases := []struct {
		name       string
		feeCalc    FeeCalculator
		txns       Transactions
		sortedTxns Transactions
	}{
		{
			name:       "already sorted",
			txns:       Transactions{txns[0], txns[1]},
			sortedTxns: Transactions{txns[0], txns[1]},
			feeCalc: func(txn *Transaction) (uint64, error) {
				return 1e8 - txn.Out[0].Hours, nil
			},
		},

		{
			name:       "reverse sorted",
			txns:       Transactions{txns[1], txns[0]},
			sortedTxns: Transactions{txns[0], txns[1]},
			feeCalc: func(txn *Transaction) (uint64, error) {
				return 1e8 - txn.Out[0].Hours, nil
			},
		},

		{
			name:       "hash tiebreaker",
			txns:       Transactions{hashSortedTxns[1], hashSortedTxns[0]},
			sortedTxns: Transactions{hashSortedTxns[0], hashSortedTxns[1]},
			feeCalc: func(txn *Transaction) (uint64, error) {
				return 1e8, nil
			},
		},

		{
			name:       "invalid fee multiplication is capped",
			txns:       Transactions{txns[1], txns[2], txns[0]},
			sortedTxns: Transactions{txns[2], txns[0], txns[1]},
			feeCalc: func(txn *Transaction) (uint64, error) {
				if txn.Hash() == txns[2].Hash() {
					return math.MaxUint64 / 2, nil
				}
				return 1e8 - txn.Out[0].Hours, nil
			},
		},

		{
			name:       "failed fee calc is filtered",
			txns:       Transactions{txns[1], txns[2], txns[0]},
			sortedTxns: Transactions{txns[0], txns[1]},
			feeCalc: func(txn *Transaction) (uint64, error) {
				if txn.Hash() == txns[2].Hash() {
					return 0, errors.New("fee calc failed")
				}
				return 1e8 - txn.Out[0].Hours, nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			txns, err := SortTransactions(tc.txns, tc.feeCalc)
			require.NoError(t, err)
			require.Equal(t, tc.sortedTxns, txns)
		})
	}
}

func TestUnsignedEstimatedSize(t *testing.T) {
	multisigsTxn := makeTransaction(t)
	multisigsTxn.Sigs = append(multisigsTxn.Sigs, make([]cipher.Sig, 3)...)
	multisigsTxn.In = append(multisigsTxn.In, make([]cipher.SHA256, 3)...)

	cases := []struct {
		name string
		txn  Transaction
	}{
		{
			name: "1 sig",
			txn:  makeTransaction(t),
		},
		{
			name: "4 sigs",
			txn:  multisigsTxn,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NotEmpty(t, tc.txn.Sigs)

			_, err := tc.txn.UnsignedEstimatedSize()
			testutil.RequireError(t, err, "Transaction is signed")

			s, err := tc.txn.Size()
			require.NoError(t, err)

			sigsLen := len(tc.txn.Sigs)
			tc.txn.Sigs = nil

			u, err := tc.txn.UnsignedEstimatedSize()
			require.NoError(t, err)

			require.Equal(t, s, u, "%d != %d", s, u)

			s2, err := tc.txn.Size()
			require.NoError(t, err)
			require.True(t, s2 < s)
			require.Equal(t, uint32(len(cipher.Sig{})*sigsLen), s-s2)
		})
	}
}
