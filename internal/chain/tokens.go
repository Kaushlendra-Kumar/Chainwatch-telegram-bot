package chain

var evmTokens = map[int64]map[string]string{
	1: {
		"0xdac17f958d2ee523a2206206994597c13d831ec7": "USDT",
		"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48": "USDC",
		"0x6b175474e89094c44da98b954eedeac495271d0f": "DAI",
		"0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2": "WETH",
		"0x2260fac5e5542a773aa44fbcfedf7c193bc2c599": "WBTC",
		"0x514910771af9ca656af840dff83e8264ecf986ca": "LINK",
		"0x1f9840a85d5af5bf1d1762f925bdaddc4201f984": "UNI",
		"0x95ad61b0a150d79219dcf64e1e6cc01f0b64c4ce": "SHIB",
		"0x6982508145454ce325ddbe47a25d4ec3d2311933": "PEPE",
	},
	137: {
		"0xc2132d05d31c914a87c6611c10748aeb04b58e8f": "USDT",
		"0x3c499c542cef5e3811e1192ce70d8cc03d5c3359": "USDC",
		"0x2791bca1f2de4661ed88a30c99a7a9449aa84174": "USDC",
		"0x7ceb23fd6bc0add59e62ac25578270cff1b9f619": "WETH",
		"0x0d500b1d8e8ef31e21c99d1db9a6444d3adf1270": "WPOL",
	},
	42161: {
		"0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9": "USDT",
		"0xaf88d065e77c8cc2239327c5edb3a432268e5831": "USDC",
		"0x82af49447d8a07e3bd95bd0d56f35241523fbab1": "WETH",
		"0x912ce59144191c1204e64559fe8253a0e49e6548": "ARB",
	},
}

var solanaMints = map[string]string{
	"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v": "USDC",
	"Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB": "USDT",
	"So11111111111111111111111111111111111111112":  "WSOL",
	"JUPyiwrYJFskUPiHa7hkeR8VUtAeFoSYbKedZNsDvCN":  "JUP",
	"DezXAZ8z7PnrnRJjz3wXBoRgixCa6xjnB7YaB1pPB263": "BONK",
}
