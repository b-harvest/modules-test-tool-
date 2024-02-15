package clictx

import (
	"github.com/b-harvest/modules-test-tool/codec"

	sdkclient "github.com/cosmos/cosmos-sdk/client"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	rpcclient "github.com/cometbft/cometbft/rpc/client"
)

// Client wraps Cosmos SDK client context.
type Client struct {
	sdkclient.Context
}

// NewClient creates Cosmos SDK client.
func NewClient(rpcURL string, rpcClient rpcclient.Client) *Client {
	cliCtx := sdkclient.Context{}.
		WithNodeURI(rpcURL).
		WithClient(rpcClient).
		WithAccountRetriever(authtypes.AccountRetriever{}).
		WithLegacyAmino(codec.EncodingConfig.Amino).
		WithTxConfig(codec.EncodingConfig.TxConfig).
		WithInterfaceRegistry(codec.EncodingConfig.InterfaceRegistry)

	return &Client{cliCtx}
}
