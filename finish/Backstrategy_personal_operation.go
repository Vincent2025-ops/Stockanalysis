package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// **回測績效結構體**（儲存每個策略的回測結果）
type Performance struct {
	Strategy     string  // 策略名稱（如 RSI、KD、MACD、SMA、Momentum、Volume Ratio、Bollinger Bands）
	TotalReturn  float64 // 總報酬率（%）
	MaxDrawdown  float64 // 最大回撤（歷史最高資本減去歷史最低資本的跌幅）
	WinRate      float64 // 勝率（%）（成功交易的比例）
	FinalCapital float64 // 最終資金（回測結束時的總資本）
}

// **讀取 CSV 檔案，解析日期、開盤價、收盤價與成交量數據**
// 支援 TWSE 官方格式 (9欄)、Yahoo/App 匯出格式 (6欄/7欄)，具備自動適配能力
func readCSV(filename string) ([]string, []float64, []float64, []float64, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, nil, nil, nil, err
	}

	var dates []string
	var opens []float64
	var prices []float64
	var volumes []float64

	for i, row := range rows {
		if i == 0 || len(row) < 5 {
			continue // 跳過標題列或無效欄位
		}

		var openP, closeP, vol float64
		dateStr := strings.TrimSpace(row[0])

		if len(row) >= 9 { // 證交所原始 STOCK_DAY 格式
			openP, _ = strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(row[3], ",", ""), "X", ""), 64)
			closeP, _ = strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(row[6], ",", ""), "X", ""), 64)
			vol, _ = strconv.ParseFloat(strings.ReplaceAll(row[1], ",", ""), 64)
		} else if len(row) == 6 { // 標準 6 欄: Date, Open, High, Low, Close, Volume
			openP, _ = strconv.ParseFloat(strings.ReplaceAll(row[1], ",", ""), 64)
			closeP, _ = strconv.ParseFloat(strings.ReplaceAll(row[4], ",", ""), 64)
			vol, _ = strconv.ParseFloat(strings.ReplaceAll(row[5], ",", ""), 64)
		} else if len(row) >= 7 { // 7 欄格式 (含 Adj Close)
			openP, _ = strconv.ParseFloat(strings.ReplaceAll(row[1], ",", ""), 64)
			closeP, _ = strconv.ParseFloat(strings.ReplaceAll(row[4], ",", ""), 64)
			vol, _ = strconv.ParseFloat(strings.ReplaceAll(row[6], ",", ""), 64)
		}

		if closeP > 0 {
			if openP <= 0 {
				openP = closeP // 若開盤價缺失則以收盤價替代
			}
			dates = append(dates, dateStr)
			opens = append(opens, openP)
			prices = append(prices, closeP)
			volumes = append(volumes, vol)
		}
	}

	return dates, opens, prices, volumes, nil
}

// **計算最大回撤 (Max Drawdown, MDD)**
func maxDrawdown(profitHistory []float64) float64 {
	if len(profitHistory) == 0 {
		return 0
	}
	maxPeak := profitHistory[0]
	maxDD := 0.0
	for _, value := range profitHistory {
		if value > maxPeak {
			maxPeak = value
		}
		drawdown := (maxPeak - value) / maxPeak
		if drawdown > maxDD {
			maxDD = drawdown
		}
	}
	return maxDD
}

// **計算真實 Wilder's RSI（相對強弱指標，範圍 0~100）**
func calculateRSI(prices []float64, period int) []float64 {
	rsi := make([]float64, len(prices))
	if len(prices) <= period {
		return rsi
	}

	gain, loss := 0.0, 0.0
	for i := 1; i <= period; i++ {
		change := prices[i] - prices[i-1]
		if change > 0 {
			gain += change
		} else {
			loss -= change
		}
	}

	avgGain := gain / float64(period)
	avgLoss := loss / float64(period)

	if avgLoss == 0 {
		rsi[period] = 100.0
	} else {
		rs := avgGain / avgLoss
		rsi[period] = 100.0 - (100.0 / (1.0 + rs))
	}

	for i := period + 1; i < len(prices); i++ {
		change := prices[i] - prices[i-1]
		g, l := 0.0, 0.0
		if change > 0 {
			g = change
		} else {
			l = -change
		}

		avgGain = (avgGain*float64(period-1) + g) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + l) / float64(period)

		if avgLoss == 0 {
			rsi[i] = 100.0
		} else {
			rs := avgGain / avgLoss
			rsi[i] = 100.0 - (100.0 / (1.0 + rs))
		}
	}
	return rsi
}

// **計算簡單移動平均線（SMA）**
func calculateSMA(prices []float64, period int) []float64 {
	sma := make([]float64, len(prices))
	for i := period - 1; i < len(prices); i++ {
		sum := 0.0
		for j := i - period + 1; j <= i; j++ {
			sum += prices[j]
		}
		sma[i] = sum / float64(period)
	}
	return sma
}

// **計算真實 MACD（指數平滑異同移動平均線）**
func calculateMACD(prices []float64, shortPeriod, longPeriod, signalPeriod int) ([]float64, []float64) {
	n := len(prices)
	macd := make([]float64, n)
	signal := make([]float64, n)

	if n < longPeriod {
		return macd, signal
	}

	kShort := 2.0 / float64(shortPeriod+1)
	kLong := 2.0 / float64(longPeriod+1)
	kSig := 2.0 / float64(signalPeriod+1)

	emaShort := prices[0]
	emaLong := prices[0]
	for i := 0; i < n; i++ {
		emaShort = prices[i]*kShort + emaShort*(1.0-kShort)
		emaLong = prices[i]*kLong + emaLong*(1.0-kLong)
		macd[i] = emaShort - emaLong
	}

	sigEMA := macd[0]
	for i := 0; i < n; i++ {
		sigEMA = macd[i]*kSig + sigEMA*(1.0-kSig)
		signal[i] = sigEMA
	}

	return macd, signal
}

// **計算布林通道（Bollinger Bands：20MA, 2 倍標準差）**
func calculateBollingerBands(prices []float64, period int) ([]float64, []float64) {
	upperBand := make([]float64, len(prices))
	lowerBand := make([]float64, len(prices))
	sma := calculateSMA(prices, period)

	for i := period - 1; i < len(prices); i++ {
		sumSquares := 0.0
		for j := i - period + 1; j <= i; j++ {
			diff := prices[j] - sma[i]
			sumSquares += diff * diff
		}
		stdDev := math.Sqrt(sumSquares / float64(period))
		upperBand[i] = sma[i] + 2*stdDev
		lowerBand[i] = sma[i] - 2*stdDev
	}

	return upperBand, lowerBand
}

// **計算 Momentum（動量指標：當前收盤價 - N日前收盤價）**
func calculateMomentum(prices []float64, period int) []float64 {
	momentum := make([]float64, len(prices))
	for i := period; i < len(prices); i++ {
		momentum[i] = prices[i] - prices[i-period]
	}
	return momentum
}

// **計算 Volume Ratio（10日均量比值：當日成交量 / 過去10日均量基準）**
func calculateVolumeRatio(volumes []float64, period int) []float64 {
	volRatio := make([]float64, len(volumes))
	for i := period; i < len(volumes); i++ {
		sum := 0.0
		// 計算過去 10 個交易日的成交量總和（不含當日）
		for j := i - period; j < i; j++ {
			sum += volumes[j]
		}
		avgVolume := sum / float64(period)
		if avgVolume > 0 {
			volRatio[i] = volumes[i] / avgVolume
		} else {
			volRatio[i] = 0.0
		}
	}
	return volRatio
}

// **計算 KD 指標（隨機指標，採標準 9 日週期）**
func calculateKD(prices []float64, period int) ([]float64, []float64) {
	k := make([]float64, len(prices))
	d := make([]float64, len(prices))

	for i := period - 1; i < len(prices); i++ {
		low := prices[i]
		high := prices[i]
		for j := i - period + 1; j <= i; j++ {
			if prices[j] < low {
				low = prices[j]
			}
			if prices[j] > high {
				high = prices[j]
			}
		}

		if high != low {
			rsv := (prices[i] - low) / (high - low) * 100
			if i == period-1 {
				k[i] = (2.0*50.0 + rsv) / 3.0
				d[i] = (2.0*50.0 + k[i]) / 3.0
			} else {
				k[i] = (2.0*k[i-1] + rsv) / 3.0
				d[i] = (2.0*d[i-1] + k[i]) / 3.0
			}
		} else {
			if i == period-1 {
				k[i] = 50.0
				d[i] = 50.0
			} else {
				k[i] = k[i-1]
				d[i] = d[i-1]
			}
		}
	}

	return k, d
}

// **回測邏輯**
func backtest(dates []string, opens []float64, prices []float64, volumes []float64, strategyName string) Performance {
	capital := 1000000.0 // 初始資金 100 萬
	position := 0.0      // 持倉數量
	buyPrice := 0.0      // 買入價格
	lastBuyDate := ""    // 上次買入日期
	var profitHistory []float64
	var wins, losses, trades int

	sma5 := calculateSMA(prices, 5)
	sma10 := calculateSMA(prices, 10) // 供均量比策略判斷 10MA 跌破
	sma20 := calculateSMA(prices, 20)
	upperBB, lowerBB := calculateBollingerBands(prices, 20)
	k, d := calculateKD(prices, 9)
	rsi := calculateRSI(prices, 14)
	momentum := calculateMomentum(prices, 10)
	volRatio := calculateVolumeRatio(volumes, 10)

	var macd, signal []float64
	if len(prices) >= 26 {
		macd, signal = calculateMACD(prices, 12, 26, 9)
	}

	fmt.Printf("\n開始回測策略: %s... 初始資金 100 萬\n", strategyName)

	for i := 0; i < len(prices); i++ {
		shouldBuy := false
		shouldSell := false

		isMACDReady := i >= 26

		// **買入條件**
		if position == 0 && capital >= prices[i] && capital >= 10000 {
			switch strategyName {
			case "SMA":
				if i >= 5 && sma5[i] > sma20[i] && sma5[i-1] <= sma20[i-1] {
					shouldBuy = true
				}
			case "MACD":
				if isMACDReady && i > 0 && macd[i] > signal[i] && macd[i-1] <= signal[i-1] {
					shouldBuy = true
				}
			case "Bollinger Bands":
				if i >= 20 && prices[i] < lowerBB[i] {
					shouldBuy = true
				}
			case "KD":
				if i >= 9 && k[i] > d[i] && k[i-1] <= d[i-1] {
					shouldBuy = true
				}
			case "RSI":
				if i >= 14 && rsi[i] < 30 {
					shouldBuy = true
				}
			case "Momentum":
				if i >= 10 && momentum[i] > 0 && momentum[i-1] <= 0 {
					shouldBuy = true
				}
			case "Volume Ratio", "成交量均量比策略（Volume Ratio）", "ChipRatio":
				// 🎯 買進條件修改：10 日均量比值 > 1.5 (帶量突破發動)
				if i >= 10 && volRatio[i] > 1.5 {
					shouldBuy = true
				}
			}

			if shouldBuy {
				position = capital / prices[i]
				buyPrice = prices[i]
				capital = 0
				lastBuyDate = dates[i]
				fmt.Printf("[交易紀錄]\n買入日期: %s  價格: %.2f  持倉數: %.2f  現有資金: %.2f\n", dates[i], prices[i], position, capital)
			}
		}

		// **賣出條件**
		if position > 0 && lastBuyDate != "" && dates[i] > lastBuyDate {
			switch strategyName {
			case "SMA":
				if i >= 5 && sma5[i] < sma20[i] && sma5[i-1] >= sma20[i-1] {
					shouldSell = true
				}
			case "MACD":
				if isMACDReady && i > 0 && macd[i] < signal[i] && macd[i-1] >= signal[i-1] {
					shouldSell = true
				}
			case "Bollinger Bands":
				if i >= 20 && prices[i] > upperBB[i] {
					shouldSell = true
				}
			case "KD":
				if i >= 9 && k[i] < d[i] && k[i-1] >= d[i-1] {
					shouldSell = true
				}
			case "RSI":
				if i >= 14 && rsi[i] > 70 {
					shouldSell = true
				}
			case "Momentum":
				if i >= 10 && momentum[i] < 0 && momentum[i-1] >= 0 {
					shouldSell = true
				}
			case "Volume Ratio", "成交量均量比策略（Volume Ratio）", "ChipRatio":
				// 🎯 平倉條件修改：
				// 1. 收盤價跌破 10MA
				breakBelow10MA := i >= 10 && sma10[i] > 0 && prices[i] < sma10[i]

				// 2. 均量比 > 2.0 且為實體長黑 K (收黑且跌幅 >= 1.5%)
				isBlackK := prices[i] < opens[i]
				isLongBlackK := isBlackK && (opens[i]-prices[i])/opens[i] >= 0.015
				if opens[i] == 0 && i > 0 { // 防呆：開盤價缺失時以昨收比對
					isLongBlackK = prices[i] < prices[i-1] && (prices[i-1]-prices[i])/prices[i-1] >= 0.02
				}
				heavyVolumeDump := i >= 10 && volRatio[i] > 2.0 && isLongBlackK

				if breakBelow10MA || heavyVolumeDump {
					shouldSell = true
				}
			}

			if shouldSell {
				sellAmount := position * prices[i]
				profit := sellAmount - (position * buyPrice)
				capital = sellAmount
				fmt.Printf("賣出日期: %s  價格: %.2f  獲利: %.2f  現有資金: %.2f\n", dates[i], prices[i], profit, capital)
				fmt.Println("---------------------------------------------")
				position = 0
				lastBuyDate = ""
				trades++
				if profit > 0 {
					wins++
				} else {
					losses++
				}
				profitHistory = append(profitHistory, capital)
			}
		}
	}

	if position > 0 {
		capital = position * prices[len(prices)-1]
		a := prices[len(prices)-1]
		fmt.Printf("尚未賣出，最後股價: %.2f\n", a)
		fmt.Printf("尚未賣出，當前資產總額: %.2f\n", capital)
		position = 0
	}

	if len(profitHistory) == 0 {
		profitHistory = append(profitHistory, capital)
	}

	totalReturn := (capital - 1000000) / 1000000 * 100
	winRate := 0.0
	if trades > 0 {
		winRate = float64(wins) / float64(trades) * 100
	}
	maxDD := maxDrawdown(profitHistory)

	fmt.Printf("回測結束，最終資金: %.2f\n", capital)

	return Performance{
		Strategy:     strategyName,
		TotalReturn:  totalReturn,
		MaxDrawdown:  maxDD,
		WinRate:      winRate,
		FinalCapital: capital,
	}
}

// **主程式**
func main() {
	dates, opens, prices, volumes, err := readCSV("2330_stock_data.csv")
	if err != nil {
		fmt.Println("讀取 CSV 失敗:", err)
		return
	}

	rsiPerf := backtest(dates, opens, prices, volumes, "RSI")
	kdPerf := backtest(dates, opens, prices, volumes, "KD")
	macdPerf := backtest(dates, opens, prices, volumes, "MACD")
	smaPerf := backtest(dates, opens, prices, volumes, "SMA")
	momentumPerf := backtest(dates, opens, prices, volumes, "Momentum")
	volRatioPerf := backtest(dates, opens, prices, volumes, "成交量均量比策略（Volume Ratio）")
	bollingerPerf := backtest(dates, opens, prices, volumes, "Bollinger Bands")

	// **列出績效**
	fmt.Println("\n📊 **技術指標回測績效比較** 📊\n初始資金:100萬元")
	fmt.Printf("%-24s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		rsiPerf.Strategy, rsiPerf.TotalReturn, rsiPerf.MaxDrawdown*100, rsiPerf.WinRate, rsiPerf.FinalCapital)
	fmt.Printf("%-24s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		kdPerf.Strategy, kdPerf.TotalReturn, kdPerf.MaxDrawdown*100, kdPerf.WinRate, kdPerf.FinalCapital)
	fmt.Printf("%-24s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		macdPerf.Strategy, macdPerf.TotalReturn, macdPerf.MaxDrawdown*100, macdPerf.WinRate, macdPerf.FinalCapital)
	fmt.Printf("%-24s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		smaPerf.Strategy, smaPerf.TotalReturn, smaPerf.MaxDrawdown*100, smaPerf.WinRate, smaPerf.FinalCapital)
	fmt.Printf("%-24s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		momentumPerf.Strategy, momentumPerf.TotalReturn, momentumPerf.MaxDrawdown*100, momentumPerf.WinRate, momentumPerf.FinalCapital)
	fmt.Printf("%-24s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		volRatioPerf.Strategy, volRatioPerf.TotalReturn, volRatioPerf.MaxDrawdown*100, volRatioPerf.WinRate, volRatioPerf.FinalCapital)
	fmt.Printf("%-24s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		bollingerPerf.Strategy, bollingerPerf.TotalReturn, bollingerPerf.MaxDrawdown*100, bollingerPerf.WinRate, bollingerPerf.FinalCapital)
}