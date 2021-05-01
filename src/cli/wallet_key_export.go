package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/skycoin/skycoin/src/cipher"
	"github.com/skycoin/skycoin/src/cipher/bip32"
	"github.com/skycoin/skycoin/src/cipher/bip39"
	"github.com/skycoin/skycoin/src/cipher/bip44"
	"github.com/skycoin/skycoin/src/wallet"
)

func walletKeyExportCmd() *cobra.Command {
	walletKeyExportCmd := &cobra.Command{
		Args:  cobra.ExactArgs(1),
		RunE:  walletKeyExportHandler,
		Use:   "walletKeyExport [wallet]",
		Short: "Export a specific key from an HD wallet",
		Long: `This command prints the xpub or xprv key for a given
    HDNode in a bip44 wallet. The HDNode path is specified with --path.
    This path is the <account/change> portion of the bip44 path.

    Please make sure that the node has wallet seed API enabled (--enable-api-sets="INSECURE_WALLET_SEED").

    Example: -k xpub --path=0 prints the account 0 xpub
    Example: -k xpub --path=0/0 prints the account 0, external chain xpub
    Example: -k xprv --path=0/1 prints the account 0, change chain xprv
    Example: -k pub --path=0/0/9 prints the account 0, external chain child 9 public key
    Example: -k prv --path=0/1/8 prints the account 0, change chain child 8 private key

    The bip32 path node apostrophe is implicit for the first element of the path.

    Use caution when using the "-p" command. If you have command
    history enabled your wallet encryption password can be recovered
    from the history log. If you do not include the "-p" option you will
    be prompted to enter your password after you enter your command.`,
	}

	walletKeyExportCmd.Flags().StringP("key", "k", "xpub", "key type (\"xpub\", \"xprv\", \"pub\", \"prv\")")
	walletKeyExportCmd.Flags().StringP("path", "", "0/0", "bip44 account'/change subpath")
	walletKeyExportCmd.Flags().StringP("password", "p", "", "wallet password")

	return walletKeyExportCmd
}

func walletKeyExportHandler(c *cobra.Command, args []string) error {
	keyType, err := c.Flags().GetString("key")
	if err != nil {
		return err
	}
	if err := validateKeyType(keyType); err != nil {
		return err
	}

	id := args[0]
	wlt, err := apiClient.Wallet(id)
	if err != nil {
		return err
	}

	if wlt.Meta.Type != wallet.WalletTypeBip44 {
		return errors.New("unsupported wallet type for key export command")
	}

	var password []byte
	if wlt.Meta.Encrypted {
		pr := NewPasswordReader([]byte(c.Flag("password").Value.String()))
		var err error
		password, err = pr.Password()
		if err != nil {
			return err
		}
	}
	rsp, err := apiClient.WalletSeed(id, string(password))
	if err != nil {
		return err
	}

	seed, err := bip39.NewSeed(rsp.Seed, rsp.SeedPassphrase)
	if err != nil {
		return err
	}

	coin, err := bip44.NewCoin(seed, *wlt.Meta.Bip44Coin)
	if err != nil {
		return err
	}

	path, err := c.Flags().GetString("path")
	if err != nil {
		return err
	}

	nodes, err := parsePath(path)
	if err != nil {
		return err
	}
	if len(nodes) > 3 {
		return errors.New("path can have at most 3 elements")
	}

	acct, err := coin.Account(nodes[0])
	if err != nil {
		return err
	}

	if len(nodes) == 1 {
		return printKey(keyType, acct.PrivateKey)
	}

	change, err := acct.NewPrivateChildKey(nodes[1])
	if err != nil {
		return err
	}

	if len(nodes) == 2 {
		return printKey(keyType, change)
	}

	child, err := change.NewPrivateChildKey(nodes[2])
	if err != nil {
		return err
	}

	if len(nodes) == 3 {
		return printKey(keyType, child)
	}

	return nil
}

func validateKeyType(kt string) error {
	switch kt {
	case "xpub", "xprv", "pub", "prv":
	default:
		return errors.New("key must be \"xpub\", \"xprv\", \"pub\" or \"prv\"")
	}

	return nil
}

func printKey(kt string, k *bip32.PrivateKey) error {
	if err := validateKeyType(kt); err != nil {
		return err
	}

	switch kt {
	case "xpub":
		fmt.Println(k.PublicKey().String())
	case "xprv":
		fmt.Println(k.String())
	case "pub":
		fmt.Println(cipher.MustNewPubKey(k.PublicKey().Key).Hex())
	case "prv":
		fmt.Println(cipher.MustNewSecKey(k.Key).Hex())
	default:
		panic("unhandled key type")
	}

	return nil
}

func parsePath(p string) ([]uint32, error) {
	pts := strings.Split(p, "/")
	idx := make([]uint32, len(pts))
	for i, c := range pts {
		x, err := strconv.ParseUint(c, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid path node number %q at position %d", c, i)
		}
		idx[i] = uint32(x)
	}

	return idx, nil
}
