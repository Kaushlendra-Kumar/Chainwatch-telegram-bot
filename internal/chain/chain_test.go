package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	vitalik = "0xd8da6bf26964af9d7eed9e03e53415d37aa96045"
	other   = "0x1111111111111111111111111111111111111111"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestFromBaseUnits(t *testing.T) {
	v, err := FromBaseUnits("1500000000000000000", 18)
	if err != nil || !near(v, 1.5) {
		t.Fatalf("got %v %v", v, err)
	}
	v, _ = FromBaseUnits("2500000", 6)
	if !near(v, 2.5) {
		t.Fatalf("got %v", v)
	}
	if _, err := FromBaseUnits("abc", 6); err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizeAddresses(t *testing.T) {
	evm := NewEVM(EVMNetworks["eth"], nil)
	if a, err := evm.Normalize("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"); err != nil || a != vitalik {
		t.Fatalf("evm normalize: %q %v", a, err)
	}
	if _, err := evm.Normalize("0x123"); err == nil {
		t.Fatal("expected invalid evm address")
	}

	btc := NewBitcoin("http://unused")
	for _, a := range []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", "BC1QAR0SRRR7XFKVY5L643LYDNW9RE59GTZZWF5MDQ"} {
		if _, err := btc.Normalize(a); err != nil {
			t.Fatalf("btc normalize %s: %v", a, err)
		}
	}
	if _, err := btc.Normalize("0xabc"); err == nil {
		t.Fatal("expected invalid btc address")
	}

	sol := NewSolana("http://unused", 1)
	if _, err := sol.Normalize("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"); err != nil {
		t.Fatalf("sol normalize: %v", err)
	}
	if _, err := sol.Normalize("0OIl0OIl0OIl0OIl0OIl0OIl0OIl0OIl"); err == nil {
		t.Fatal("expected invalid sol address")
	}
}

func TestRegistryAliases(t *testing.T) {
	r := NewRegistry(NewEVM(EVMNetworks["eth"], nil), NewBitcoin("x"), NewSolana("x", 1))
	for alias, want := range map[string]string{"ethereum": "eth", "BTC": "btc", "solana": "sol"} {
		tr, ok := r.Get(alias)
		if !ok || tr.ID() != want {
			t.Fatalf("alias %s -> %v", alias, tr)
		}
	}
	if _, ok := r.Get("doge"); ok {
		t.Fatal("unexpected chain")
	}
}

func TestBuildEVMEvents(t *testing.T) {
	native := []etherscanTx{
		{BlockNumber: "102", TimeStamp: "1700000100", Hash: "0xb", From: vitalik, To: other, Value: "2000000000000000000"},
		{BlockNumber: "101", TimeStamp: "1700000050", Hash: "0xfail", From: other, To: vitalik, Value: "1", IsError: "1"},
		{BlockNumber: "100", TimeStamp: "1700000000", Hash: "0xa", From: other, To: strings.ToUpper(vitalik[:2]) + vitalik[2:], Value: "1000000000000000000"},
		{BlockNumber: "99", TimeStamp: "1700000000", Hash: "0xzero", From: vitalik, To: other, Value: "0"},
	}
	tokens := []etherscanTx{
		{BlockNumber: "105", TimeStamp: "1700000200", Hash: "0xc", From: other, To: vitalik, Value: "2500000", ContractAddress: "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", TokenSymbol: "USDC", TokenDecimal: "6"},
		{BlockNumber: "104", TimeStamp: "1700000150", Hash: "0xd", From: other, To: vitalik, Value: "1000", ContractAddress: "0xdeadbeef00000000000000000000000000000000", TokenSymbol: "U$DT<script>", TokenDecimal: "0"},
	}
	events, maxBlock := buildEVMEvents(vitalik, EVMNetworks["eth"], native, tokens)
	if maxBlock != 105 {
		t.Fatalf("maxBlock = %d", maxBlock)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}
	if events[0].TxHash != "0xa" || events[0].Direction != In || !near(events[0].Amount, 1) || events[0].Counterparty != other {
		t.Fatalf("event0 = %+v", events[0])
	}
	if events[1].TxHash != "0xb" || events[1].Direction != Out || !near(events[1].Amount, 2) {
		t.Fatalf("event1 = %+v", events[1])
	}
	if events[3].Symbol != "USDC" || !events[3].Verified || !near(events[3].Amount, 2.5) {
		t.Fatalf("usdc event = %+v", events[3])
	}
	if events[2].Verified || events[2].Symbol != "UDTSCRIPT" {
		t.Fatalf("spoofed token should be unverified and sanitized: %+v", events[2])
	}
}

func TestBitcoinPoll(t *testing.T) {
	owner := "bc1qowner000000000000000000000000000000"
	mk := func(id string, height int64, inAddr string, inVal int64, outs map[string]int64) map[string]any {
		var vout []map[string]any
		for a, v := range outs {
			vout = append(vout, map[string]any{"scriptpubkey_address": a, "value": v})
		}
		return map[string]any{
			"txid":   id,
			"vin":    []map[string]any{{"prevout": map[string]any{"scriptpubkey_address": inAddr, "value": inVal}}},
			"vout":   vout,
			"status": map[string]any{"confirmed": height > 0, "block_height": height, "block_time": 1700000000 + height},
		}
	}
	txs := []map[string]any{
		mk("pending", 0, "bc1qsender", 5000, map[string]int64{owner: 5000}),
		mk("new-out", 801, owner, 100000, map[string]int64{"bc1qdest": 60000, owner: 39000}),
		mk("new-in", 800, "bc1qsender", 250000000, map[string]int64{owner: 150000000, "bc1qchange": 99990000}),
		mk("old", 799, "bc1qsender", 1000, map[string]int64{owner: 1000}),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != fmt.Sprintf("/address/%s/txs", owner) {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(txs)
	}))
	defer srv.Close()

	b := NewBitcoin(srv.URL)
	events, cursor, err := b.Poll(context.Background(), owner, "799")
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "801" {
		t.Fatalf("cursor = %s", cursor)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %+v", events)
	}
	if events[0].TxHash != "new-in" || events[0].Direction != In || !near(events[0].Amount, 1.5) || events[0].Counterparty != "bc1qsender" {
		t.Fatalf("in event = %+v", events[0])
	}
	if events[1].TxHash != "new-out" || events[1].Direction != Out || !near(events[1].Amount, 0.00061) || events[1].Counterparty != "bc1qdest" {
		t.Fatalf("out event = %+v", events[1])
	}
}

func TestBuildSolanaEvents(t *testing.T) {
	owner := "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	raw := `{
		"slot": 300,
		"blockTime": 1700000000,
		"meta": {
			"err": null,
			"fee": 5000,
			"preBalances": [2000000000, 100, 0],
			"postBalances": [999995000, 1000000100, 0],
			"preTokenBalances": [{"accountIndex": 2, "mint": "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", "owner": "` + owner + `", "uiTokenAmount": {"amount": "1000000", "decimals": 6}}],
			"postTokenBalances": [{"accountIndex": 2, "mint": "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", "owner": "` + owner + `", "uiTokenAmount": {"amount": "4000000", "decimals": 6}}]
		},
		"transaction": {"message": {"accountKeys": [{"pubkey": "` + owner + `"}, {"pubkey": "DestPubkey111111111111111111111111111111111"}, {"pubkey": "TokenAcct"}]}}
	}`
	var tx solTx
	if err := json.Unmarshal([]byte(raw), &tx); err != nil {
		t.Fatal(err)
	}
	events := buildSolanaEvents(owner, "sig1", tx)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %+v", events)
	}
	if events[0].Direction != Out || !near(events[0].Amount, 1) || events[0].Counterparty != "DestPubkey111111111111111111111111111111111" {
		t.Fatalf("sol event = %+v", events[0])
	}
	if events[1].Symbol != "USDT" || events[1].Direction != In || !near(events[1].Amount, 3) || !events[1].Verified {
		t.Fatalf("token event = %+v", events[1])
	}
}
