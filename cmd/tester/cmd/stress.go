package cmd

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/evmos/ethermint/app"
	"github.com/evmos/ethermint/crypto/hd"
	"github.com/evmos/ethermint/encoding"
	etherminttypes "github.com/evmos/ethermint/types"
	evmtypes "github.com/evmos/ethermint/x/evm/types"
	rpcclient "github.com/tendermint/tendermint/rpc/client"

	"github.com/b-harvest/modules-test-tool/client"
	"github.com/b-harvest/modules-test-tool/config"
)

type AccountDispenser struct {
	c            *client.Client
	mnemonics    []string
	i            int
	addr         string
	privKey      cryptotypes.PrivKey
	ecdsaPrivKey *ecdsa.PrivateKey
	accSeq       uint64
	accNum       uint64
	evmDenom     string
}

func NewAccountDispenser(c *client.Client, mnemonics []string, canto_addr string) *AccountDispenser {
	return &AccountDispenser{
		c:         c,
		mnemonics: mnemonics,
		addr:      canto_addr,
	}
}

func (d *AccountDispenser) Next() error {
	mnemonic := d.mnemonics[d.i]
	bz, err := hd.EthSecp256k1.Derive()(mnemonic, keyring.DefaultBIP39Passphrase, etherminttypes.BIP44HDPath)
	if err != nil {
		return err
	}
	privKey := hd.EthSecp256k1.Generate()(bz)
	ecdsaPrivKey, err := crypto.ToECDSA(privKey.Bytes())
	if err != nil {
		return err
	}

	d.privKey = privKey
	d.ecdsaPrivKey = ecdsaPrivKey
	acc, err := d.c.GRPC.GetBaseAccountInfo(context.Background(), d.addr)
	if err != nil {
		return fmt.Errorf("get base account info: %w", err)
	}
	d.accSeq = acc.GetSequence()
	d.accNum = acc.GetAccountNumber()
	d.i++
	if d.i >= len(d.mnemonics) {
		d.i = 0
	}

	evmParams, err := d.c.GRPC.GetEvmParams(context.Background())
	if err != nil {
		return err
	}
	d.evmDenom = evmParams.EvmDenom

	return nil
}

func (d *AccountDispenser) Addr() string {
	return d.addr
}

func (d *AccountDispenser) PrivKey() cryptotypes.PrivKey {
	return d.privKey
}

func (d *AccountDispenser) AccSeq() uint64 {
	return d.accSeq
}

func (d *AccountDispenser) AccNum() uint64 {
	return d.accNum
}

func (d *AccountDispenser) IncAccSeq() uint64 {
	r := d.accSeq
	d.accSeq++
	return r
}

func (d *AccountDispenser) DecAccSeq() {
	d.accSeq--
}

type Scenario struct {
	Rounds         int
	NumTxsPerBlock int
}

var (
	scenarios = []Scenario{
		{2000, 20},
		{2000, 50},
		{2000, 200},
		{2000, 500},
	}
	//scenarios = []Scenario{
	//	{5, 10},
	//	{5, 50},
	//	{5, 100},
	//	{5, 200},
	//	{5, 300},
	//	{5, 400},
	//	{5, 500},
	//}
)

func StressTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stress-test [calldata] [contract-address] [amount]",
		Short: "run stress test",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			ctx := context.Background()

			encodingConfig := encoding.MakeConfig(app.ModuleBasics)
			txConfig := encodingConfig.TxConfig

			err := SetLogger(logLevel)
			if err != nil {
				return fmt.Errorf("set logger: %w", err)
			}

			cfg, err := config.Read(config.DefaultConfigPath)
			if err != nil {
				return fmt.Errorf("read config: %w", err)
			}

			client, err := client.NewClient(cfg.RPC.Address, cfg.GRPC.Address)
			if err != nil {
				return fmt.Errorf("new client: %w", err)
			}
			defer client.Stop() // nolint: errcheck

			// make calldata
			//
			// var NativeMetaData = &bind.MetaData{
			// 	 ABI: "[{\"inputs\":[],\"name\":\"add\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"subtract\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getCounter\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
			// }
			//
			// func main() {
			// 	 abi, err := NativeMetaData.GetAbi()
			// 	 if err != nil {
			// 	 	panic(err)
			// 	 }
			// 	 payload, err := abi.Pack("add")
			// 	 if err != nil {
			// 	 	panic(err)
			// 	 }
			// 	 fmt.Println("Calldata in hex format:", hex.EncodeToString(payload))
			// }
			calldata, err := hexutil.Decode(args[0])
			if err != nil {
				return fmt.Errorf("failed to decode ethereum tx hex bytes: %w", err)
			}

			contractAddr := common.HexToAddress(args[1])

			amount, ok := new(big.Int).SetString(args[2], 10)
			if !ok {
				return fmt.Errorf("failed to conver %s to big.Int", args[2])
			}

			gasLimit := uint64(cfg.Custom.GasLimit)
			gasPrice := big.NewInt(cfg.Custom.GasPrice)

			d := NewAccountDispenser(client, cfg.Custom.Mnemonics, cfg.Custom.CantoAddress)
			if err := d.Next(); err != nil {
				return fmt.Errorf("get next account: %w", err)
			}

			blockTimes := make(map[int64]time.Time)

			for no, scenario := range scenarios {
				st, err := client.RPC.Status(ctx)
				if err != nil {
					return fmt.Errorf("get status: %w", err)
				}
				startingHeight := st.SyncInfo.LatestBlockHeight + 2
				log.Info().Msgf("current block height is %d, waiting for the next block to be committed", st.SyncInfo.LatestBlockHeight)

				if err := rpcclient.WaitForHeight(client.RPC, startingHeight-1, nil); err != nil {
					return fmt.Errorf("wait for height: %w", err)
				}
				log.Info().Msgf("starting simulation #%d, rounds = %d, num txs per block = %d", no+1, scenario.Rounds, scenario.NumTxsPerBlock)

				targetHeight := startingHeight

				for i := 0; i < scenario.Rounds; i++ {
					st, err := client.RPC.Status(ctx)
					if err != nil {
						return fmt.Errorf("get status: %w", err)
					}
					if st.SyncInfo.LatestBlockHeight != targetHeight-1 {
						log.Warn().Int64("expected", targetHeight-1).Int64("got", st.SyncInfo.LatestBlockHeight).Msg("mismatching block height")
						targetHeight = st.SyncInfo.LatestBlockHeight + 1
					}

					started := time.Now()
					sent := 0
				loop:
					for sent < scenario.NumTxsPerBlock {
						for sent < scenario.NumTxsPerBlock {
							accSeq := d.IncAccSeq()
							unsignedTx := gethtypes.NewTransaction(accSeq, contractAddr, amount, gasLimit, gasPrice, calldata)
							signedTx, err := gethtypes.SignTx(unsignedTx, gethtypes.NewEIP155Signer(big.NewInt(cfg.Custom.ChainID)), d.ecdsaPrivKey)
							if err != nil {
								return err
							}

							ethTxBytes, err := rlp.EncodeToBytes(signedTx)
							if err != nil {
								return err
							}

							msg := &evmtypes.MsgEthereumTx{}
							if err := msg.UnmarshalBinary(ethTxBytes); err != nil {
								return err
							}

							if err := msg.ValidateBasic(); err != nil {
								return err
							}

							tx, err := msg.BuildTx(txConfig.NewTxBuilder(), d.evmDenom)
							if err != nil {
								return err
							}

							txBytes, err := txConfig.TxEncoder()(tx)
							if err != nil {
								return fmt.Errorf("sign tx: %w", err)
							}

							resp, err := client.GRPC.BroadcastTx(ctx, txBytes)
							if err != nil {
								return fmt.Errorf("broadcast tx: %w", err)
							}

							if resp.TxResponse.Code != 0 {
								if resp.TxResponse.Code == 0x14 {
									log.Warn().Msg("mempool is full, stopping")
									d.DecAccSeq()
									break loop
								}
								if resp.TxResponse.Code == 0x13 || resp.TxResponse.Code == 0x20 {
									if err := d.Next(); err != nil {
										return fmt.Errorf("get next account: %w", err)
									}
									log.Warn().Str("addr", d.Addr()).Uint64("seq", d.AccSeq()).Msgf("received %#v, using next account", resp.TxResponse)
									time.Sleep(500 * time.Millisecond)
									break
								} else {
									panic(fmt.Sprintf("%#v\n", resp.TxResponse))
								}
							}
							sent++
						}
					}
					log.Debug().Msgf("took %s broadcasting txs", time.Since(started))

					if err := rpcclient.WaitForHeight(client.RPC, targetHeight, nil); err != nil {
						return fmt.Errorf("wait for height: %w", err)
					}

					r, err := client.RPC.Block(ctx, &targetHeight)
					if err != nil {
						return err
					}
					var blockDuration time.Duration
					bt, ok := blockTimes[targetHeight-1]
					if !ok {
						log.Warn().Msg("past block time not found")
					} else {
						blockDuration = r.Block.Time.Sub(bt)
						delete(blockTimes, targetHeight-1)
					}
					blockTimes[targetHeight] = r.Block.Time
					log.Info().
						Int64("height", targetHeight).
						Str("block-time", r.Block.Time.Format(time.RFC3339Nano)).
						Str("block-duration", blockDuration.String()).
						Int("broadcast-txs", sent).
						Int("committed-txs", len(r.Block.Txs)).
						Msg("block committed")

					targetHeight++
				}

				started := time.Now()
				log.Debug().Msg("cooling down")
				for {
					st, err := client.RPC.NumUnconfirmedTxs(ctx)
					if err != nil {
						return fmt.Errorf("get status: %w", err)
					}
					if st.Total == 0 {
						break
					}
					time.Sleep(5 * time.Second)
				}
				log.Debug().Str("elapsed", time.Since(started).String()).Msg("done cooling down")
				time.Sleep(time.Minute)
			}

			return nil
		},
	}
	return cmd
}
