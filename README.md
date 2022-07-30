## TON domain bot

## BE CAREFUL! The bot is just beginning to be tested. There may be some bugs.

### Description

The main purpose of the bot is to maintain domain bids within the maximum bid.
You need to fill config file **only for those domains that have already deployed**!
If domain is not deployed yet buy it from dns.ton.org.

### How to use
1. Fill config parameters
   * seed - seed phrase for wallet
   * collection_address - root resolver address. You send message to root contract when deploy new domain. (use default for mainnet)
   * domains - list of domains and maximum bid (in Grams. TON = 10^9 Grams) for each
2. Set ```WalletType = wallet.V4R2 // WRITE YOU WALLET TYPE HERE``` you wallet version.
3. For run TVM you need libs from tongo (https://github.com/startfellows/tongo/tree/master/lib)
4. Client connects to mainnet by default, if you need use TESTNET use: ```client, err := liteclient.NewClient(nil)```

### Logic

1. For each domain runs separate worker (with time shift)
2. Every 5 minutes worker check domain contract and extract: max bid value, who place bid and auction end time
3. If last bid address != you wallet then try to place new bid
4. New_bid value = 1.051 * prev_bid
5. If new_bid < wallet_balance and new_bid<max_bid from config - place bid
