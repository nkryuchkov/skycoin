package integration_test

import (
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/skycoin/skycoin/src/api"
	"github.com/skycoin/skycoin/src/cipher"
	"github.com/skycoin/skycoin/src/coin"
	"github.com/skycoin/skycoin/src/params"
	"github.com/skycoin/skycoin/src/readable"
	"github.com/skycoin/skycoin/src/testutil"
	"github.com/skycoin/skycoin/src/util/droplet"
	"github.com/skycoin/skycoin/src/util/fee"
	"github.com/skycoin/skycoin/src/wallet"
)

func TestStableInjectTransaction(t *testing.T) {
	if !doStable(t) {
		return
	}

	c := newClient()

	cases := []struct {
		name string
		txn  coin.Transaction
		code int
		err  string
	}{
		{
			name: "database is read only",
			txn:  coin.Transaction{},
			code: http.StatusInternalServerError,
			err:  "500 Internal Server Error - database is in read-only mode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := c.InjectTransaction(&tc.txn)
			if tc.err != "" {
				assertResponseError(t, err, tc.code, tc.err)
				return
			}

			require.NoError(t, err)

			// Result should be a valid txid
			require.NotEmpty(t, result)
			h, err := cipher.SHA256FromHex(result)
			require.NoError(t, err)
			require.NotEqual(t, cipher.SHA256{}, h)
		})
	}
}

func TestLiveInjectTransactionDisableNetworking(t *testing.T) {
	if !doLive(t) {
		return
	}

	if !liveDisableNetworking(t) {
		t.Skip("Networking must be disabled for this test")
		return
	}

	requireWalletEnv(t)

	c := newClient()

	w, totalCoins, totalHours, password := prepareAndCheckWallet(t, c, 2e6, 20)

	defaultChangeAddress := w.Entries[0].Address.String()

	type testCase struct {
		name         string
		createTxnReq api.WalletCreateTransactionRequest
		err          string
		code         int
	}

	cases := []testCase{
		{
			name: "valid request, networking disabled",
			err:  "503 Service Unavailable - Outgoing connections are disabled",
			code: http.StatusServiceUnavailable,
			createTxnReq: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins),
						Hours:   fmt.Sprint(totalHours / 2),
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			txnResp, err := c.WalletCreateTransaction(tc.createTxnReq)
			require.NoError(t, err)

			txid, err := c.InjectEncodedTransaction(txnResp.EncodedTransaction)
			if tc.err != "" {
				assertResponseError(t, err, tc.code, tc.err)

				// A second injection will fail with the same error,
				// since the transaction should not be saved to the DB
				_, err = c.InjectEncodedTransaction(txnResp.EncodedTransaction)
				assertResponseError(t, err, tc.code, tc.err)
				return
			}

			require.NotEmpty(t, txid)
			require.Equal(t, txnResp.Transaction.TxID, txid)

			h, err := cipher.SHA256FromHex(txid)
			require.NoError(t, err)
			require.NotEqual(t, cipher.SHA256{}, h)
		})
	}
}

func TestLiveInjectTransactionEnableNetworking(t *testing.T) {
	if !doLive(t) {
		return
	}

	if liveDisableNetworking(t) {
		t.Skip("This tests requires networking enabled")
		return
	}

	requireWalletEnv(t)

	c := newClient()
	w, totalCoins, _, password := prepareAndCheckWallet(t, c, 2e6, 2)

	defaultChangeAddress := w.Entries[0].Address.String()

	tt := []struct {
		name         string
		createTxnReq api.WalletCreateTransactionRequest
		checkTxn     func(t *testing.T, tx *readable.TransactionWithStatus)
	}{
		{
			name: "send all coins to the first address",
			createTxnReq: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type:        wallet.HoursSelectionTypeAuto,
					Mode:        wallet.HoursSelectionModeShare,
					ShareFactor: "1",
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[0].Address.String(),
						Coins:   toDropletString(t, totalCoins),
					},
				},
			},
			checkTxn: func(t *testing.T, tx *readable.TransactionWithStatus) {
				// Confirms the total output coins are equal to the totalCoins
				var coins uint64
				for _, o := range tx.Transaction.Out {
					c, err := droplet.FromString(o.Coins)
					require.NoError(t, err)
					coins, err = coin.AddUint64(coins, c)
					require.NoError(t, err)
				}

				// Confirms the address balance are equal to the totalCoins
				coins, _ = getAddressBalance(t, c, w.Entries[0].Address.String())
				require.Equal(t, totalCoins, coins)
			},
		},
		{
			// send 0.003 coin to the second address,
			// this amount is chosen to not interfere with TestLiveWalletCreateTransaction
			name: "send 0.003 coin to second address",
			createTxnReq: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type:        wallet.HoursSelectionTypeAuto,
					Mode:        wallet.HoursSelectionModeShare,
					ShareFactor: "0.5",
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, 3e3),
					},
				},
			},
			checkTxn: func(t *testing.T, tx *readable.TransactionWithStatus) {
				// Confirms there're two outputs, one to the second address, one as change output to the first address.
				require.Len(t, tx.Transaction.Out, 2)

				// Gets the output of the second address in the transaction
				getAddrOutputInTxn := func(t *testing.T, tx *readable.TransactionWithStatus, addr string) *readable.TransactionOutput {
					for _, output := range tx.Transaction.Out {
						if output.Address == addr {
							return &output
						}
					}
					t.Fatalf("transaction doesn't have output to address: %v", addr)
					return nil
				}

				out := getAddrOutputInTxn(t, tx, w.Entries[1].Address.String())

				// Confirms the second address has 0.003 coin
				require.Equal(t, out.Coins, "0.003000")
				require.Equal(t, out.Address, w.Entries[1].Address.String())

				coin, err := droplet.FromString(out.Coins)
				require.NoError(t, err)

				// Gets the expected change coins
				expectChangeCoins := totalCoins - coin

				// Gets the real change coins
				changeOut := getAddrOutputInTxn(t, tx, w.Entries[0].Address.String())
				changeCoins, err := droplet.FromString(changeOut.Coins)
				require.NoError(t, err)
				// Confirms the change coins are matched.
				require.Equal(t, expectChangeCoins, changeCoins)
			},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			txnResp, err := c.WalletCreateTransaction(tc.createTxnReq)
			require.NoError(t, err)

			txid, err := c.InjectEncodedTransaction(txnResp.EncodedTransaction)
			require.NoError(t, err)
			require.Equal(t, txnResp.Transaction.TxID, txid)

			tk := time.NewTicker(time.Second)
			var txn *readable.TransactionWithStatus
		loop:
			for {
				select {
				case <-time.After(30 * time.Second):
					t.Fatal("Waiting for transaction to be confirmed timeout")
				case <-tk.C:
					txn = getTransaction(t, c, txnResp.Transaction.TxID)
					if txn.Status.Confirmed {
						break loop
					}
				}
			}
			tc.checkTxn(t, txn)
		})
	}
}

func TestLiveWalletSignTransaction(t *testing.T) {
	if !doLive(t) {
		return
	}

	requireWalletEnv(t)

	c := newClient()

	w, _, _, password := prepareAndCheckWallet(t, c, 2e6, 20)

	// Fetch outputs held by the wallet
	addrs := make([]string, len(w.Entries))
	for i, e := range w.Entries {
		addrs[i] = e.SkycoinAddress().String()
	}

	summary, err := c.OutputsForAddresses(addrs)
	require.NoError(t, err)
	// Abort if the transaction is spending summary
	require.Empty(t, summary.OutgoingOutputs)
	// Need at least 2 summary for the created transaction
	require.True(t, len(summary.HeadOutputs) > 1)

	// Use the first two outputs for a transaction
	headOutputs := summary.HeadOutputs[:2]
	outputs, err := headOutputs.ToUxArray()
	require.NoError(t, err)
	totalCoins, err := outputs.Coins()
	require.NoError(t, err)
	totalCoinsStr, err := droplet.ToString(totalCoins)
	require.NoError(t, err)

	uxOutHashes := make([]string, len(outputs))
	for i, o := range outputs {
		uxOutHashes[i] = o.Hash().Hex()
	}

	// Create an unsigned transaction using two inputs
	// Ensure at least 2 inputs
	// Specify outputs in the request to create txn
	// Specify unsigned in the request to create txn
	txnResp, err := c.WalletCreateTransaction(api.WalletCreateTransactionRequest{
		Unsigned: true,
		HoursSelection: api.HoursSelection{
			Type:        wallet.HoursSelectionTypeAuto,
			Mode:        wallet.HoursSelectionModeShare,
			ShareFactor: "0.5",
		},
		Wallet: api.WalletCreateTransactionRequestWallet{
			ID:       w.Filename(),
			Password: password,
			UxOuts:   uxOutHashes,
		},
		To: []api.Receiver{
			{
				Address: w.Entries[0].SkycoinAddress().String(),
				Coins:   totalCoinsStr,
			},
		},
	})
	require.NoError(t, err)

	type testCase struct {
		name        string
		req         api.WalletSignTransactionRequest
		fullySigned bool
		err         string
		code        int
	}

	cases := []testCase{
		{
			name: "sign one input",
			req: api.WalletSignTransactionRequest{
				WalletID:           w.Filename(),
				Password:           password,
				SignIndexes:        []int{1},
				EncodedTransaction: txnResp.EncodedTransaction,
			},
			fullySigned: false,
		},
		{
			name: "sign all inputs",
			req: api.WalletSignTransactionRequest{
				WalletID:           w.Filename(),
				Password:           password,
				SignIndexes:        nil,
				EncodedTransaction: txnResp.EncodedTransaction,
			},
			fullySigned: true,
		},
	}

	doTest := func(tc testCase) {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := c.WalletSignTransaction(tc.req)
			if tc.err != "" {
				assertResponseError(t, err, tc.code, tc.err)
				return
			}

			require.NoError(t, err)

			txnBytes, err := hex.DecodeString(tc.req.EncodedTransaction)
			require.NoError(t, err)
			txn, err := coin.TransactionDeserialize(txnBytes)
			require.NoError(t, err)

			// TxID should have changed
			require.NotEqual(t, txn.Hash(), resp.Transaction.TxID)
			// Length, InnerHash should not have changed
			require.Equal(t, txn.Length, resp.Transaction.Length)
			require.Equal(t, txn.InnerHash.Hex(), resp.Transaction.InnerHash)

			_, err = c.VerifyTransaction(api.VerifyTransactionRequest{
				EncodedTransaction: resp.EncodedTransaction,
				Unsigned:           false,
			})
			if tc.fullySigned {
				require.NoError(t, err)
			} else {
				testutil.RequireError(t, err, "Transaction violates hard constraint: Unsigned input in transaction")
			}

			_, err = c.VerifyTransaction(api.VerifyTransactionRequest{
				EncodedTransaction: resp.EncodedTransaction,
				Unsigned:           true,
			})
			if tc.fullySigned {
				testutil.RequireError(t, err, "Transaction violates hard constraint: Unsigned transaction must contain a null signature")
			} else {
				require.NoError(t, err)
			}
		})
	}

	for _, tc := range cases {
		doTest(tc)
	}

	// Create a partially signed transaction then sign the remainder of it
	resp, err := c.WalletSignTransaction(api.WalletSignTransactionRequest{
		WalletID:           w.Filename(),
		Password:           password,
		SignIndexes:        []int{1},
		EncodedTransaction: txnResp.EncodedTransaction,
	})
	require.NoError(t, err)

	doTest(testCase{
		name: "sign partially signed transaction",
		req: api.WalletSignTransactionRequest{
			WalletID:           w.Filename(),
			Password:           password,
			EncodedTransaction: resp.EncodedTransaction,
		},
		fullySigned: true,
	})
}

func toDropletString(t *testing.T, i uint64) string {
	x, err := droplet.ToString(i)
	require.NoError(t, err)
	return x
}

func TestLiveWalletCreateTransactionSpecificUnsigned(t *testing.T) {
	testLiveWalletCreateTransactionSpecific(t, true)
}

func TestLiveWalletCreateTransactionSpecificSigned(t *testing.T) {
	testLiveWalletCreateTransactionSpecific(t, false)
}

func testLiveWalletCreateTransactionSpecific(t *testing.T, unsigned bool) {
	if !doLive(t) {
		return
	}

	requireWalletEnv(t)

	c := newClient()

	w, totalCoins, totalHours, password := prepareAndCheckWallet(t, c, 2e6, 20)

	remainingHours := fee.RemainingHours(totalHours, params.UserVerifyTxn.BurnFactor)
	require.True(t, remainingHours > 1)

	addresses := make([]string, len(w.Entries))
	addressMap := make(map[string]struct{}, len(w.Entries))
	for i, e := range w.Entries {
		addresses[i] = e.Address.String()
		addressMap[e.Address.String()] = struct{}{}
	}

	// Get all outputs
	outputs, err := c.Outputs()
	require.NoError(t, err)

	// Split outputs into those held by the wallet and those not
	var walletOutputHashes []string
	var walletOutputs readable.UnspentOutputs
	walletAuxs := make(map[string][]string)
	var nonWalletOutputs readable.UnspentOutputs
	for _, o := range outputs.HeadOutputs {
		if _, ok := addressMap[o.Address]; ok {
			walletOutputs = append(walletOutputs, o)
			walletOutputHashes = append(walletOutputHashes, o.Hash)
			walletAuxs[o.Address] = append(walletAuxs[o.Address], o.Hash)
		} else {
			nonWalletOutputs = append(nonWalletOutputs, o)
		}
	}

	require.NotEmpty(t, walletOutputs)
	require.NotEmpty(t, nonWalletOutputs)

	unknownOutput := testutil.RandSHA256(t)
	defaultChangeAddress := w.Entries[0].Address.String()

	type testCase struct {
		name                 string
		req                  api.WalletCreateTransactionRequest
		outputs              []coin.TransactionOutput
		outputsSubset        []coin.TransactionOutput
		err                  string
		code                 int
		ignoreHours          bool
		additionalRespVerify func(t *testing.T, r *api.CreateTransactionResponse)
	}

	cases := []testCase{
		{
			name: "invalid decimals",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[0].Address.String(),
						Coins:   "0.0001",
						Hours:   "1",
					},
				},
			},
			err:  "400 Bad Request - to[0].coins has too many decimal places",
			code: http.StatusBadRequest,
		},

		{
			name: "overflowing hours",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[0].Address.String(),
						Coins:   "0.001",
						Hours:   "1",
					},
					{
						Address: w.Entries[0].Address.String(),
						Coins:   "0.001",
						Hours:   fmt.Sprint(uint64(math.MaxUint64)),
					},
					{
						Address: w.Entries[0].Address.String(),
						Coins:   "0.001",
						Hours:   fmt.Sprint(uint64(math.MaxUint64) - 1),
					},
				},
			},
			err:  "400 Bad Request - total output hours error: uint64 addition overflow",
			code: http.StatusBadRequest,
		},

		{
			name: "insufficient coins",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[0].Address.String(),
						Coins:   fmt.Sprint(totalCoins + 1),
						Hours:   "1",
					},
				},
			},
			err:  "400 Bad Request - balance is not sufficient",
			code: http.StatusBadRequest,
		},

		{
			name: "insufficient hours",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[0].Address.String(),
						Coins:   toDropletString(t, totalCoins),
						Hours:   fmt.Sprint(totalHours + 1),
					},
				},
			},
			err:  "400 Bad Request - hours are not sufficient",
			code: http.StatusBadRequest,
		},

		{
			// NOTE: this test will fail if "totalCoins - 1e3" does not require
			// all of the outputs to be spent, e.g. if there is an output with
			// "totalCoins - 1e3" coins in it.
			// TODO -- Check that the wallet does not have an output of 0.001,
			// because then this test cannot be performed, since there is no
			// way to use all outputs and produce change in that case.
			name: "valid request, manual one output with change, spend all",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins-1e3),
						Hours:   "1",
					},
				},
			},
			outputs: []coin.TransactionOutput{
				{
					Address: w.Entries[1].SkycoinAddress(),
					Coins:   totalCoins - 1e3,
					Hours:   1,
				},
				{
					Address: w.Entries[0].SkycoinAddress(),
					Coins:   1e3,
					Hours:   remainingHours - 1,
				},
			},
		},

		{
			// NOTE: this test will fail if "totalCoins - 1e3" does not require
			// all of the outputs to be spent, e.g. if there is an output with
			// "totalCoins - 1e3" coins in it.
			// TODO -- Check that the wallet does not have an output of 0.001,
			// because then this test cannot be performed, since there is no
			// way to use all outputs and produce change in that case.
			name: "valid request, manual one output with change, spend all, unspecified change address",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins-1e3),
						Hours:   "1",
					},
				},
			},
			outputs: []coin.TransactionOutput{
				{
					Address: w.Entries[1].SkycoinAddress(),
					Coins:   totalCoins - 1e3,
					Hours:   1,
				},
				{
					// Address omitted -- will be checked later in the test body
					Coins: 1e3,
					Hours: remainingHours - 1,
				},
			},
		},

		{
			name: "valid request, manual one output with change, don't spend all",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, 1e3),
						Hours:   "1",
					},
				},
			},
			outputsSubset: []coin.TransactionOutput{
				{
					Address: w.Entries[1].SkycoinAddress(),
					Coins:   1e3,
					Hours:   1,
				},
				// NOTE: change omitted,
				// change is too difficult to predict in this case, we are
				// just checking that not all uxouts get spent in the transaction
			},
		},

		{
			name: "valid request, manual one output no change",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins),
						Hours:   "1",
					},
				},
			},
			outputs: []coin.TransactionOutput{
				{
					Address: w.Entries[1].SkycoinAddress(),
					Coins:   totalCoins,
					Hours:   1,
				},
			},
		},

		{
			// NOTE: no reliable way to test the ignore unconfirmed behavior,
			// this test only checks that if IgnoreUnconfirmed is specified,
			// the API doesn't throw up some parsing error
			name: "valid request, manual one output no change, ignore unconfirmed",
			req: api.WalletCreateTransactionRequest{
				IgnoreUnconfirmed: true,
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins),
						Hours:   "1",
					},
				},
			},
			outputs: []coin.TransactionOutput{
				{
					Address: w.Entries[1].SkycoinAddress(),
					Coins:   totalCoins,
					Hours:   1,
				},
			},
		},

		{
			name: "valid request, auto one output no change, share factor recalculates to 1.0",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type:        wallet.HoursSelectionTypeAuto,
					Mode:        wallet.HoursSelectionModeShare,
					ShareFactor: "0.5",
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins),
					},
				},
			},
			outputs: []coin.TransactionOutput{
				{
					Address: w.Entries[1].SkycoinAddress(),
					Coins:   totalCoins,
					Hours:   remainingHours,
				},
			},
		},

		{
			name: "valid request, auto two outputs with change",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type:        wallet.HoursSelectionTypeAuto,
					Mode:        wallet.HoursSelectionModeShare,
					ShareFactor: "0.5",
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, 1e3),
					},
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins-2e3),
					},
				},
			},
			outputs: []coin.TransactionOutput{
				{
					Address: w.Entries[1].SkycoinAddress(),
					Coins:   1e3,
				},
				{
					Address: w.Entries[1].SkycoinAddress(),
					Coins:   totalCoins - 2e3,
				},
				{
					Address: w.Entries[0].SkycoinAddress(),
					Coins:   1e3,
				},
			},
			ignoreHours: true, // the hours are too unpredictable
		},

		{
			name: "uxout does not exist",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
					UxOuts:   []string{unknownOutput.Hex()},
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins),
						Hours:   "1",
					},
				},
			},
			err:  fmt.Sprintf("400 Bad Request - unspent output of %s does not exist", unknownOutput.Hex()),
			code: http.StatusBadRequest,
		},

		{
			name: "uxout not held by the wallet",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
					UxOuts:   []string{nonWalletOutputs[0].Hash},
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins),
						Hours:   "1",
					},
				},
			},
			err:  "400 Bad Request - uxout is not owned by any address in the wallet",
			code: http.StatusBadRequest,
		},

		{
			name: "insufficient balance with uxouts",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
					UxOuts:   []string{walletOutputs[0].Hash},
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins+1e3),
						Hours:   "1",
					},
				},
			},
			err:  "400 Bad Request - balance is not sufficient",
			code: http.StatusBadRequest,
		},

		{
			// NOTE: expects wallet to have multiple outputs with non-zero coins
			name: "insufficient hours with uxouts",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
					UxOuts:   []string{walletOutputs[0].Hash},
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, 1e3),
						Hours:   fmt.Sprint(totalHours + 1),
					},
				},
			},
			err:  "400 Bad Request - hours are not sufficient",
			code: http.StatusBadRequest,
		},

		{
			name: "valid request, uxouts specified",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
					// NOTE: all uxouts are provided, which has the same behavior as
					// not providing any uxouts or addresses.
					// Using a subset of uxouts makes the wallet setup very
					// difficult, especially to make deterministic, in the live test
					// More complex cases should be covered by unit tests
					UxOuts: walletOutputHashes,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins-1e3),
						Hours:   "1",
					},
				},
			},
			outputs: []coin.TransactionOutput{
				{
					Address: w.Entries[1].SkycoinAddress(),
					Coins:   totalCoins - 1e3,
					Hours:   1,
				},
				{
					Address: w.Entries[0].SkycoinAddress(),
					Coins:   1e3,
					Hours:   remainingHours - 1,
				},
			},
			additionalRespVerify: func(t *testing.T, r *api.CreateTransactionResponse) {
				require.Equal(t, len(walletOutputHashes), len(r.Transaction.In))
			},
		},

		{
			name: "specified addresses not in wallet",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:        w.Filename(),
					Password:  password,
					Addresses: []string{testutil.MakeAddress().String()},
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins),
						Hours:   "1",
					},
				},
			},
			err:  "400 Bad Request - address not found in wallet",
			code: http.StatusBadRequest,
		},

		{
			name: "valid request, addresses specified",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password,
					// NOTE: all addresses are provided, which has the same behavior as
					// not providing any addresses.
					// Using a subset of addresses makes the wallet setup very
					// difficult, especially to make deterministic, in the live test
					// More complex cases should be covered by unit tests
					Addresses: addresses,
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[1].Address.String(),
						Coins:   toDropletString(t, totalCoins-1e3),
						Hours:   "1",
					},
				},
			},
			outputs: []coin.TransactionOutput{
				{
					Address: w.Entries[1].SkycoinAddress(),
					Coins:   totalCoins - 1e3,
					Hours:   1,
				},
				{
					Address: w.Entries[0].SkycoinAddress(),
					Coins:   1e3,
					Hours:   remainingHours - 1,
				},
			},
		},
	}

	if w.IsEncrypted() {
		cases = append(cases, testCase{
			name: "invalid password",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password + "foo",
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[0].Address.String(),
						Coins:   "1000",
						Hours:   "1",
					},
				},
			},
			err:  "400 Bad Request - invalid password",
			code: http.StatusBadRequest,
		})

		cases = append(cases, testCase{
			name: "password not provided",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: "",
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[0].Address.String(),
						Coins:   "1000",
						Hours:   "1",
					},
				},
			},
			err:  "400 Bad Request - missing password",
			code: http.StatusBadRequest,
		})

	} else {
		cases = append(cases, testCase{
			name: "password provided for unencrypted wallet",
			req: api.WalletCreateTransactionRequest{
				HoursSelection: api.HoursSelection{
					Type: wallet.HoursSelectionTypeManual,
				},
				Wallet: api.WalletCreateTransactionRequestWallet{
					ID:       w.Filename(),
					Password: password + "foo",
				},
				ChangeAddress: &defaultChangeAddress,
				To: []api.Receiver{
					{
						Address: w.Entries[0].Address.String(),
						Coins:   "1000",
						Hours:   "1",
					},
				},
			},
			err:  "400 Bad Request - wallet is not encrypted",
			code: http.StatusBadRequest,
		})
	}

	for _, tc := range cases {
		name := fmt.Sprintf("unsigned=%v %s", unsigned, tc.name)
		t.Run(name, func(t *testing.T) {
			require.False(t, len(tc.outputs) != 0 && len(tc.outputsSubset) != 0, "outputs and outputsSubset can't both be set")

			tc.req.Unsigned = unsigned
			result, err := c.WalletCreateTransaction(tc.req)
			if tc.err != "" {
				assertResponseError(t, err, tc.code, tc.err)
				return
			}

			require.NoError(t, err)

			if len(tc.outputsSubset) == 0 {
				require.Equal(t, len(tc.outputs), len(result.Transaction.Out))
			}

			for i, o := range tc.outputs {
				// The final change output may not have the address specified,
				// if the ChangeAddress was not specified in the wallet params.
				// Calculate it automatically based upon the transaction inputs
				if o.Address.Null() {
					require.Equal(t, i, len(tc.outputs)-1)
					require.Nil(t, tc.req.ChangeAddress)

					changeAddr := result.Transaction.Out[i].Address
					// The changeAddr must be associated with one of the transaction inputs
					changeAddrFound := false
					for _, x := range result.Transaction.In {
						require.NotNil(t, x.Address)
						if changeAddr == x.Address {
							changeAddrFound = true
							break
						}
					}

					require.True(t, changeAddrFound)
				} else {
					require.Equal(t, o.Address.String(), result.Transaction.Out[i].Address)
				}

				coins, err := droplet.FromString(result.Transaction.Out[i].Coins)
				require.NoError(t, err)
				require.Equal(t, o.Coins, coins, "[%d] %d != %d", i, o.Coins, coins)

				if !tc.ignoreHours {
					hours, err := strconv.ParseUint(result.Transaction.Out[i].Hours, 10, 64)
					require.NoError(t, err)
					require.Equal(t, o.Hours, hours, "[%d] %d != %d", i, o.Hours, hours)
				}
			}

			assertEncodeTxnMatchesTxn(t, result)
			assertRequestedCoins(t, tc.req.To, result.Transaction.Out)
			assertCreatedTransactionValid(t, result.Transaction, unsigned)

			if tc.req.HoursSelection.Type == wallet.HoursSelectionTypeManual {
				assertRequestedHours(t, tc.req.To, result.Transaction.Out)
			}

			if tc.additionalRespVerify != nil {
				tc.additionalRespVerify(t, result)
			}

			assertVerifyTransaction(t, c, result.EncodedTransaction, unsigned)
		})
	}
}

func TestLiveWalletCreateTransactionRandomUnsigned(t *testing.T) {
	testLiveWalletCreateTransactionRandom(t, true)
}

func TestLiveWalletCreateTransactionRandomSigned(t *testing.T) {
	testLiveWalletCreateTransactionRandom(t, false)
}

func testLiveWalletCreateTransactionRandom(t *testing.T, unsigned bool) {
	if !doLive(t) {
		return
	}

	debug := false
	tLog := func(t *testing.T, args ...interface{}) {
		if debug {
			t.Log(args...)
		}
	}
	tLogf := func(t *testing.T, msg string, args ...interface{}) {
		if debug {
			t.Logf(msg, args...)
		}
	}

	requireWalletEnv(t)

	c := newClient()

	w, totalCoins, totalHours, password := prepareAndCheckWallet(t, c, 2e6, 20)

	if w.IsEncrypted() {
		t.Skip("Skipping TestLiveWalletCreateTransactionRandom tests with encrypted wallet")
		return
	}

	remainingHours := fee.RemainingHours(totalHours, params.UserVerifyTxn.BurnFactor)
	require.True(t, remainingHours > 1)

	assertTxnOutputCount := func(t *testing.T, changeAddress string, nOutputs int, result *api.CreateTransactionResponse) {
		nResultOutputs := len(result.Transaction.Out)
		require.True(t, nResultOutputs == nOutputs || nResultOutputs == nOutputs+1)
		hasChange := nResultOutputs == nOutputs+1
		changeOutput := result.Transaction.Out[nResultOutputs-1]
		if hasChange {
			require.Equal(t, changeOutput.Address, changeAddress)
		}

		tLog(t, "hasChange", hasChange)
		if hasChange {
			tLog(t, "changeCoins", changeOutput.Coins)
			tLog(t, "changeHours", changeOutput.Hours)
		}
	}

	iterations := 250
	maxOutputs := 10
	destAddrs := make([]cipher.Address, maxOutputs)
	for i := range destAddrs {
		destAddrs[i] = testutil.MakeAddress()
	}

	for i := 0; i < iterations; i++ {
		tLog(t, "iteration", i)
		tLog(t, "totalCoins", totalCoins)
		tLog(t, "totalHours", totalHours)

		spendableHours := fee.RemainingHours(totalHours, params.UserVerifyTxn.BurnFactor)
		tLog(t, "spendableHours", spendableHours)

		coins := rand.Intn(int(totalCoins)) + 1
		coins -= coins % int(params.UserVerifyTxn.MaxDropletDivisor())
		if coins == 0 {
			coins = int(params.UserVerifyTxn.MaxDropletDivisor())
		}
		hours := rand.Intn(int(spendableHours + 1))
		nOutputs := rand.Intn(maxOutputs) + 1

		tLog(t, "sendCoins", coins)
		tLog(t, "sendHours", hours)

		changeAddress := w.Entries[0].Address.String()

		shareFactor := strconv.FormatFloat(rand.Float64(), 'f', 8, 64)

		tLog(t, "shareFactor", shareFactor)

		to := make([]api.Receiver, 0, nOutputs)
		remainingHours := hours
		remainingCoins := coins
		for i := 0; i < nOutputs; i++ {
			if remainingCoins == 0 {
				break
			}

			receiver := api.Receiver{}
			receiver.Address = destAddrs[rand.Intn(len(destAddrs))].String()

			if i == nOutputs-1 {
				var err error
				receiver.Coins, err = droplet.ToString(uint64(remainingCoins))
				require.NoError(t, err)
				receiver.Hours = fmt.Sprint(remainingHours)

				remainingCoins = 0
				remainingHours = 0
			} else {
				receiverCoins := rand.Intn(remainingCoins) + 1
				receiverCoins -= receiverCoins % int(params.UserVerifyTxn.MaxDropletDivisor())
				if receiverCoins == 0 {
					receiverCoins = int(params.UserVerifyTxn.MaxDropletDivisor())
				}

				var err error
				receiver.Coins, err = droplet.ToString(uint64(receiverCoins))
				require.NoError(t, err)
				remainingCoins -= receiverCoins

				receiverHours := rand.Intn(remainingHours + 1)
				receiver.Hours = fmt.Sprint(receiverHours)
				remainingHours -= receiverHours
			}

			to = append(to, receiver)
		}

		// Remove duplicate outputs
		dup := make(map[api.Receiver]struct{}, len(to))
		newTo := make([]api.Receiver, 0, len(dup))
		for _, o := range to {
			if _, ok := dup[o]; !ok {
				dup[o] = struct{}{}
				newTo = append(newTo, o)
			}
		}
		to = newTo

		nOutputs = len(to)
		tLog(t, "nOutputs", nOutputs)

		rand.Shuffle(len(to), func(i, j int) {
			to[i], to[j] = to[j], to[i]
		})

		for i, o := range to {
			tLogf(t, "to[%d].Hours %s\n", i, o.Hours)
		}

		autoTo := make([]api.Receiver, len(to))
		for i, o := range to {
			autoTo[i] = api.Receiver{
				Address: o.Address,
				Coins:   o.Coins,
				Hours:   "",
			}
		}

		// Remove duplicate outputs
		dup = make(map[api.Receiver]struct{}, len(autoTo))
		newAutoTo := make([]api.Receiver, 0, len(dup))
		for _, o := range autoTo {
			if _, ok := dup[o]; !ok {
				dup[o] = struct{}{}
				newAutoTo = append(newAutoTo, o)
			}
		}
		autoTo = newAutoTo

		nAutoOutputs := len(autoTo)
		tLog(t, "nAutoOutputs", nAutoOutputs)

		for i, o := range autoTo {
			tLogf(t, "autoTo[%d].Coins %s\n", i, o.Coins)
		}

		// Auto, random share factor

		result, err := c.WalletCreateTransaction(api.WalletCreateTransactionRequest{
			Unsigned: unsigned,
			HoursSelection: api.HoursSelection{
				Type:        wallet.HoursSelectionTypeAuto,
				Mode:        wallet.HoursSelectionModeShare,
				ShareFactor: shareFactor,
			},
			ChangeAddress: &changeAddress,
			Wallet: api.WalletCreateTransactionRequestWallet{
				ID:       w.Filename(),
				Password: password,
			},
			To: autoTo,
		})
		require.NoError(t, err)

		assertEncodeTxnMatchesTxn(t, result)
		assertTxnOutputCount(t, changeAddress, nAutoOutputs, result)
		assertRequestedCoins(t, autoTo, result.Transaction.Out)
		assertCreatedTransactionValid(t, result.Transaction, unsigned)
		assertVerifyTransaction(t, c, result.EncodedTransaction, unsigned)

		// Auto, share factor 0

		result, err = c.WalletCreateTransaction(api.WalletCreateTransactionRequest{
			Unsigned: unsigned,
			HoursSelection: api.HoursSelection{
				Type:        wallet.HoursSelectionTypeAuto,
				Mode:        wallet.HoursSelectionModeShare,
				ShareFactor: "0",
			},
			ChangeAddress: &changeAddress,
			Wallet: api.WalletCreateTransactionRequestWallet{
				ID:       w.Filename(),
				Password: password,
			},
			To: autoTo,
		})
		require.NoError(t, err)

		assertEncodeTxnMatchesTxn(t, result)
		assertTxnOutputCount(t, changeAddress, nAutoOutputs, result)
		assertRequestedCoins(t, autoTo, result.Transaction.Out)
		assertCreatedTransactionValid(t, result.Transaction, unsigned)
		assertVerifyTransaction(t, c, result.EncodedTransaction, unsigned)

		// Check that the non-change outputs have 0 hours
		for _, o := range result.Transaction.Out[:nAutoOutputs] {
			require.Equal(t, "0", o.Hours)
		}

		// Auto, share factor 1

		result, err = c.WalletCreateTransaction(api.WalletCreateTransactionRequest{
			Unsigned: unsigned,
			HoursSelection: api.HoursSelection{
				Type:        wallet.HoursSelectionTypeAuto,
				Mode:        wallet.HoursSelectionModeShare,
				ShareFactor: "1",
			},
			ChangeAddress: &changeAddress,
			Wallet: api.WalletCreateTransactionRequestWallet{
				ID:       w.Filename(),
				Password: password,
			},
			To: autoTo,
		})
		require.NoError(t, err)

		assertEncodeTxnMatchesTxn(t, result)
		assertTxnOutputCount(t, changeAddress, nAutoOutputs, result)
		assertRequestedCoins(t, autoTo, result.Transaction.Out)
		assertCreatedTransactionValid(t, result.Transaction, unsigned)
		assertVerifyTransaction(t, c, result.EncodedTransaction, unsigned)

		// Check that the change output has 0 hours
		if len(result.Transaction.Out) > nAutoOutputs {
			require.Equal(t, "0", result.Transaction.Out[nAutoOutputs].Hours)
		}

		// Manual

		result, err = c.WalletCreateTransaction(api.WalletCreateTransactionRequest{
			Unsigned: unsigned,
			HoursSelection: api.HoursSelection{
				Type: wallet.HoursSelectionTypeManual,
			},
			ChangeAddress: &changeAddress,
			Wallet: api.WalletCreateTransactionRequestWallet{
				ID:       w.Filename(),
				Password: password,
			},
			To: to,
		})
		require.NoError(t, err)

		assertEncodeTxnMatchesTxn(t, result)
		assertTxnOutputCount(t, changeAddress, nOutputs, result)
		assertRequestedCoins(t, to, result.Transaction.Out)
		assertRequestedHours(t, to, result.Transaction.Out)
		assertCreatedTransactionValid(t, result.Transaction, unsigned)
		assertVerifyTransaction(t, c, result.EncodedTransaction, unsigned)
	}
}

func assertEncodeTxnMatchesTxn(t *testing.T, result *api.CreateTransactionResponse) {
	require.NotEmpty(t, result.EncodedTransaction)
	emptyTxn := &coin.Transaction{}
	require.NotEqual(t, hex.EncodeToString(emptyTxn.Serialize()), result.EncodedTransaction)
	txn, err := result.Transaction.ToTransaction()
	require.NoError(t, err)

	serializedTxn := txn.Serialize()
	require.Equal(t, hex.EncodeToString(serializedTxn), result.EncodedTransaction)

	require.Equal(t, int(txn.Length), len(serializedTxn))
}

func assertRequestedCoins(t *testing.T, to []api.Receiver, out []api.CreatedTransactionOutput) {
	var requestedCoins uint64
	for _, o := range to {
		c, err := droplet.FromString(o.Coins)
		require.NoError(t, err)
		requestedCoins += c
	}

	var sentCoins uint64
	for _, o := range out[:len(to)] { // exclude change output
		c, err := droplet.FromString(o.Coins)
		require.NoError(t, err)
		sentCoins += c
	}

	require.Equal(t, requestedCoins, sentCoins)
}

func assertRequestedHours(t *testing.T, to []api.Receiver, out []api.CreatedTransactionOutput) {
	for i, o := range out[:len(to)] { // exclude change output
		toHours, err := strconv.ParseUint(to[i].Hours, 10, 64)
		require.NoError(t, err)

		outHours, err := strconv.ParseUint(o.Hours, 10, 64)
		require.NoError(t, err)
		require.Equal(t, toHours, outHours)
	}
}

func assertVerifyTransaction(t *testing.T, c *api.Client, encodedTransaction string, unsigned bool) {
	_, err := c.VerifyTransaction(api.VerifyTransactionRequest{
		EncodedTransaction: encodedTransaction,
		Unsigned:           false,
	})
	if unsigned {
		assertResponseError(t, err, http.StatusUnprocessableEntity, "Transaction violates hard constraint: Unsigned input in transaction")
	} else {
		require.NoError(t, err)
	}

	_, err = c.VerifyTransaction(api.VerifyTransactionRequest{
		EncodedTransaction: encodedTransaction,
		Unsigned:           true,
	})
	if unsigned {
		require.NoError(t, err)
	} else {
		assertResponseError(t, err, http.StatusUnprocessableEntity, "Transaction violates hard constraint: Unsigned transaction must contain a null signature")
	}
}

func assertCreatedTransactionValid(t *testing.T, r api.CreatedTransaction, unsigned bool) {
	require.NotEmpty(t, r.In)
	require.NotEmpty(t, r.Out)

	require.Equal(t, len(r.In), len(r.Sigs))
	if unsigned {
		for _, s := range r.Sigs {
			ss := cipher.MustSigFromHex(s)
			require.True(t, ss.Null())
		}
	}

	fee, err := strconv.ParseUint(r.Fee, 10, 64)
	require.NoError(t, err)

	require.NotEqual(t, uint64(0), fee)

	var inputHours uint64
	var inputCoins uint64
	for _, in := range r.In {
		require.NotNil(t, in.CalculatedHours)
		calculatedHours, err := strconv.ParseUint(in.CalculatedHours, 10, 64)
		require.NoError(t, err)
		inputHours, err = coin.AddUint64(inputHours, calculatedHours)
		require.NoError(t, err)

		require.NotNil(t, in.Hours)
		hours, err := strconv.ParseUint(in.Hours, 10, 64)
		require.NoError(t, err)

		require.True(t, hours <= calculatedHours)

		require.NotNil(t, in.Coins)
		coins, err := droplet.FromString(in.Coins)
		require.NoError(t, err)
		inputCoins, err = coin.AddUint64(inputCoins, coins)
		require.NoError(t, err)
	}

	var outputHours uint64
	var outputCoins uint64
	for _, out := range r.Out {
		hours, err := strconv.ParseUint(out.Hours, 10, 64)
		require.NoError(t, err)
		outputHours, err = coin.AddUint64(outputHours, hours)
		require.NoError(t, err)

		coins, err := droplet.FromString(out.Coins)
		require.NoError(t, err)
		outputCoins, err = coin.AddUint64(outputCoins, coins)
		require.NoError(t, err)
	}

	require.True(t, inputHours > outputHours)
	require.Equal(t, inputHours-outputHours, fee)

	require.Equal(t, inputCoins, outputCoins)

	require.Equal(t, uint8(0), r.Type)
	require.NotEmpty(t, r.Length)
}

func getTransaction(t *testing.T, c *api.Client, txid string) *readable.TransactionWithStatus {
	tx, err := c.Transaction(txid)
	if err != nil {
		t.Fatalf("%v", err)
	}

	return tx
}

// getAddressBalance gets balance of given address.
// Returns coins and coin hours.
func getAddressBalance(t *testing.T, c *api.Client, addr string) (uint64, uint64) { // nolint: unparam
	bp, err := c.Balance([]string{addr})
	if err != nil {
		t.Fatalf("%v", err)
	}
	return bp.Confirmed.Coins, bp.Confirmed.Hours
}