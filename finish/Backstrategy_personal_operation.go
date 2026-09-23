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
	Strategy     string  // 策略名稱（如 RSI、KD、MACD、SMA、Momentum、ChipRatio、Bollinger Bands）
	TotalReturn  float64 // 總報酬率（%）
	MaxDrawdown  float64 // 最大回撤（歷史最高資本減去歷史最低資本的跌幅）
	WinRate      float64 // 勝率（%）（成功交易的比例）
	FinalCapital float64 // 最終資金（回測結束時的總資本）
}

// **讀取 CSV 檔案，解析股價與成交量數據**
func readCSV(filename string) ([]string, []float64, []float64, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, nil, nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, nil, nil, err
	}

	var dates []string
	var prices []float64
	var volumes []float64

	// 解析 CSV 每一行，將日期、收盤價與成交量存入陣列
	for i, row := range rows {
		if i == 0 {
			continue // 跳過標題列
		}
		closePrice, err := strconv.ParseFloat(row[6], 64) // 取得收盤價
		if err != nil {
			continue
		}

		// 取得成交量（優先讀取 row[7]，若無則嘗試 row[5]）
		var vol float64 = 0.0
		if len(row) > 7 {
			vol, _ = strconv.ParseFloat(strings.ReplaceAll(row[7], ",", ""), 64)
		} else if len(row) > 5 {
			vol, _ = strconv.ParseFloat(strings.ReplaceAll(row[5], ",", ""), 64)
		}

		dates = append(dates, row[0])
		prices = append(prices, closePrice)
		volumes = append(volumes, vol)
	}

	return dates, prices, volumes, nil
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
// 修正：首期計算採標準簡單平均 (SMA)，後續天數採正統 Wilder 平滑法递歸累算
func calculateRSI(prices []float64, period int) []float64 {
	rsi := make([]float64, len(prices))
	if len(prices) <= period {
		return rsi
	}

	gain, loss := 0.0, 0.0

	// 1. 計算前 period 天的漲跌差額總和
	for i := 1; i <= period; i++ {
		change := prices[i] - prices[i-1]
		if change > 0 {
			gain += change
		} else {
			loss -= change
		}
	}

	// 2. 首期基準值：計算平均漲幅與平均跌幅 (SMA)
	avgGain := gain / float64(period)
	avgLoss := loss / float64(period)

	if avgLoss == 0 {
		rsi[period] = 100.0
	} else {
		rs := avgGain / avgLoss
		rsi[period] = 100.0 - (100.0 / (1.0 + rs))
	}

	// 3. 後續天數採用正統 Wilder 平滑法 (Smoothed Moving Average)
	// 公式：今日平滑值 = (前日平滑值 * (N - 1) + 今日數值) / N
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
// 修正：徹底解決原先誤用 SMA 代替 EMA 的問題，完全遵循標準 EMA 公式計算
// shortPeriod: 12, longPeriod: 26, signalPeriod: 9
func calculateMACD(prices []float64, shortPeriod, longPeriod, signalPeriod int) ([]float64, []float64) {
	n := len(prices)
	macd := make([]float64, n)   // DIF 快線 = EMA(12) - EMA(26)
	signal := make([]float64, n) // DEM 慢線 = EMA(DIF, 9)

	if n < longPeriod {
		return macd, signal
	}

	// 指數移動平均平滑係數 alpha = 2 / (Period + 1)
	kShort := 2.0 / float64(shortPeriod+1)
	kLong := 2.0 / float64(longPeriod+1)
	kSig := 2.0 / float64(signalPeriod+1)

	// 1. 計算短天期 (12) 與長天期 (26) 的 EMA，並求出 DIF
	emaShort := prices[0]
	emaLong := prices[0]
	for i := 0; i < n; i++ {
		emaShort = prices[i]*kShort + emaShort*(1.0-kShort)
		emaLong = prices[i]*kLong + emaLong*(1.0-kLong)
		macd[i] = emaShort - emaLong
	}

	// 2. 對 DIF 數列進行 9 日 EMA 平滑，求出 Signal 訊號線 (DEM)
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

// **計算 Chip Ratio（10日均量比值：當日成交量 / 過去10日均量基準）**
func calculateChipRatio(volumes []float64, period int) []float64 {
	chipRatio := make([]float64, len(volumes))

	for i := period; i < len(volumes); i++ {
		sum := 0.0
		// 計算過去 10 個交易日的成交量總和（不含當日）
		for j := i - period; j < i; j++ {
			sum += volumes[j]
		}
		avgVolume := sum / float64(period)
		if avgVolume > 0 {
			chipRatio[i] = volumes[i] / avgVolume
		} else {
			chipRatio[i] = 0.0
		}
	}

	return chipRatio
}

// **計算 KD 指標（隨機指標，採標準 9 日週期）**
// 註：正統公式需真實高低價 (High/Low)，此處基於現有收盤價資料結構，
// 於 9 日區間內取收盤價極值進行標準 RSV 與遞迴平滑運算 (K/D 預設初值為 50)
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
				k[i] = (2.0*50.0 + rsv) / 3.0 // 首期以基準 50 進行權重計算
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
func backtest(dates []string, prices []float64, volumes []float64, strategyName string) Performance {
	capital := 1000000.0 // 初始資金 100 萬
	position := 0.0      // 持倉數量
	buyPrice := 0.0      // 買入價格
	lastBuyDate := ""    // 上次買入日期
	var profitHistory []float64
	var wins, losses, trades int

	sma5 := calculateSMA(prices, 5)
	sma20 := calculateSMA(prices, 20)
	upperBB, lowerBB := calculateBollingerBands(prices, 20)
	k, d := calculateKD(prices, 9)
	rsi := calculateRSI(prices, 14)
	momentum := calculateMomentum(prices, 10)
	chipRatio := calculateChipRatio(volumes, 10)

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
				// 5MA 向上黃金交叉 20MA
				if i >= 5 && sma5[i] > sma20[i] && sma5[i-1] <= sma20[i-1] {
					shouldBuy = true
				}
			case "MACD":
				// 修正：MACD 快線 (DIF) 向上黃金交叉慢線 (Signal)
				if isMACDReady && i > 0 && macd[i] > signal[i] && macd[i-1] <= signal[i-1] {
					shouldBuy = true
				}
			case "Bollinger Bands":
				// 跌破布林下軌 (超跌逆勢撈底)
				if i >= 20 && prices[i] < lowerBB[i] {
					shouldBuy = true
				}
			case "KD":
				// K值 向上黃金交叉 D值
				if i >= 9 && k[i] > d[i] && k[i-1] <= d[i-1] {
					shouldBuy = true
				}
			case "RSI":
				// RSI 落入超賣區間 (< 30)
				if i >= 14 && rsi[i] < 30 {
					shouldBuy = true
				}
			case "Momentum":
				// 修正：動量指標由負翻正 (向上突破 0 軸)
				if i >= 10 && momentum[i] > 0 && momentum[i-1] <= 0 {
					shouldBuy = true
				}
			case "ChipRatio":
				// 均量比大於 0.5 (主力帶量)
				if i >= 10 && chipRatio[i] > 0.5 {
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
				// 5MA 向下死亡交叉 20MA
				if i >= 5 && sma5[i] < sma20[i] && sma5[i-1] >= sma20[i-1] {
					shouldSell = true
				}
			case "MACD":
				// 修正：MACD 快線 (DIF) 向下死亡交叉慢線 (Signal)
				if isMACDReady && i > 0 && macd[i] < signal[i] && macd[i-1] >= signal[i-1] {
					shouldSell = true
				}
			case "Bollinger Bands":
				// 突破布林上軌 (達到滿足點獲利了結)
				if i >= 20 && prices[i] > upperBB[i] {
					shouldSell = true
				}
			case "KD":
				// K值 向下死亡交叉 D值
				if i >= 9 && k[i] < d[i] && k[i-1] >= d[i-1] {
					shouldSell = true
				}
			case "RSI":
				// RSI 達到超買區間 (> 70)
				if i >= 14 && rsi[i] > 70 {
					shouldSell = true
				}
			case "Momentum":
				// 修正：動量指標由正轉負 (向下跌破 0 軸)
				if i >= 10 && momentum[i] < 0 && momentum[i-1] >= 0 {
					shouldSell = true
				}
			case "ChipRatio":
				// 均量比縮至 0.5 以下平倉
				if i >= 10 && chipRatio[i] < 0.5 {
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

	// 若回測結束時仍持有庫存，依最後一天收盤價計算當前淨值
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
	dates, prices, volumes, err := readCSV("2330_stock_data.csv")
	if err != nil {
		fmt.Println("讀取 CSV 失敗:", err)
		return
	}

	rsiPerf := backtest(dates, prices, volumes, "RSI")
	kdPerf := backtest(dates, prices, volumes, "KD")
	macdPerf := backtest(dates, prices, volumes, "MACD")
	smaPerf := backtest(dates, prices, volumes, "SMA")
	momentumPerf := backtest(dates, prices, volumes, "Momentum")
	chipratioPerf := backtest(dates, prices, volumes, "ChipRatio")
	bollingerPerf := backtest(dates, prices, volumes, "Bollinger Bands")

	// **列出績效**
	fmt.Println("\n📊 **技術指標回測績效比較** 📊\n初始資金:100萬元")
	fmt.Printf("%-15s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		rsiPerf.Strategy, rsiPerf.TotalReturn, rsiPerf.MaxDrawdown*100, rsiPerf.WinRate, rsiPerf.FinalCapital)
	fmt.Printf("%-15s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		kdPerf.Strategy, kdPerf.TotalReturn, kdPerf.MaxDrawdown*100, kdPerf.WinRate, kdPerf.FinalCapital)
	fmt.Printf("%-15s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		macdPerf.Strategy, macdPerf.TotalReturn, macdPerf.MaxDrawdown*100, macdPerf.WinRate, macdPerf.FinalCapital)
	fmt.Printf("%-15s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		smaPerf.Strategy, smaPerf.TotalReturn, smaPerf.MaxDrawdown*100, smaPerf.WinRate, smaPerf.FinalCapital)
	fmt.Printf("%-15s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		momentumPerf.Strategy, momentumPerf.TotalReturn, momentumPerf.MaxDrawdown*100, momentumPerf.WinRate, momentumPerf.FinalCapital)
	fmt.Printf("%-15s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		chipratioPerf.Strategy, chipratioPerf.TotalReturn, chipratioPerf.MaxDrawdown*100, chipratioPerf.WinRate, chipratioPerf.FinalCapital)
	fmt.Printf("%-15s | 總報酬率: %.2f%% | 最大回撤: %.2f%% | 勝率: %.2f%% | 資金總額: %.2f\n",
		bollingerPerf.Strategy, bollingerPerf.TotalReturn, bollingerPerf.MaxDrawdown*100, bollingerPerf.WinRate, bollingerPerf.FinalCapital)
}