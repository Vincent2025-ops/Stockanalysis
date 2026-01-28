package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings" // 新增這個 library 來處理CSV逗號
)

// **回測績效結構體**（儲存每個策略的回測結果）
type Performance struct {
	Strategy     string  // 策略名稱（如 RSI、KD、MACD、SMA、Momentum、ChipRatio、Bollinger Bands）
	TotalReturn  float64 // 總報酬率（%）
	MaxDrawdown  float64 // 最大回撤（歷史最高資本減去歷史最低資本的跌幅）
	WinRate      float64 // 勝率（%）（成功交易的比例）
	FinalCapital float64 // 最終資金（回測結束時的總資本）
}

// **讀取 CSV 檔案，解析股價數據**
func readCSV(filename string) ([]string, []float64, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	
	// 【關鍵修正 1】設定 FieldsPerRecord 為 -1
	//這允許每一行的欄位數量可以不一致 (忽略行尾多餘的逗號)
	reader.FieldsPerRecord = -1 

	rows, err := reader.ReadAll()
	if err != nil {
		return nil, nil, err
	}

	var dates []string
	var prices []float64

	for i, row := range rows {
		if i == 0 {
			continue // 跳過標題列
		}
		
		// 確保該行有足夠的欄位 (避免空行導致 crash)
		if len(row) < 7 {
			continue
		}

		// 【關鍵修正 2】處理數字中的千分位逗號
		// 原始資料可能是 "1,070.00"，含有逗號無法直接轉 float
		cleanPriceStr := strings.ReplaceAll(row[6], ",", "") 
		
		closePrice, err := strconv.ParseFloat(cleanPriceStr, 64)
		if err != nil {
			// 若轉換失敗(例如遇到空值)，可以選擇 log 錯誤或忽略
			continue
		}
		
		dates = append(dates, row[0]) 
		prices = append(prices, closePrice)
	}

	return dates, prices, nil
}

// **計算最大回撤**
func maxDrawdown(profitHistory []float64) float64 {
	if len(profitHistory) == 0 {
		return 0 // 如果沒有交易，回撤為 0
	}
	maxPeak := profitHistory[0] // **歷史最高資本**
	maxDD := 0.0				// **最大回撤預設為 0**
	for _, value := range profitHistory {
		if value > maxPeak {
			maxPeak = value
		}
		drawdown := (maxPeak - value) / maxPeak // **計算回撤率**
		if drawdown > maxDD {
			maxDD = drawdown
		}
	}
	return maxDD
}

// 計算 RSI（相對強弱指數）RSI 是衡量價格變動速度與變動幅度的動能指標，數值範圍在 0~100 之間。其中 RS（相對強弱）= 平均上漲點數 / 平均下跌點數（通常取 14 天計算）。
func calculateRSI(prices []float64, period int) []float64 {
	rsi := make([]float64, len(prices))
	gain, loss := 0.0, 0.0

	for i := 1; i < period; i++ {
		change := prices[i] - prices[i-1]
		if change > 0 {
			gain += change
		} else {
			loss -= change
		}
	}

	for i := period; i < len(prices); i++ {
		change := prices[i] - prices[i-1]
		if change > 0 {
			gain = (gain*(float64(period)-1) + change) / float64(period)
			loss = (loss * (float64(period) - 1)) / float64(period)
		} else {
			gain = (gain * (float64(period) - 1)) / float64(period)
			loss = (loss*(float64(period)-1) - change) / float64(period)
		}

		if loss == 0 {
			rsi[i] = 100
		} else {
			rs := gain / loss
			rsi[i] = 100 - (100 / (1 + rs))
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

// **計算 MACD**
func calculateMACD(prices []float64, shortPeriod, longPeriod, signalPeriod int) ([]float64, []float64) {
	macd := make([]float64, len(prices))
	signal := make([]float64, len(prices))
	emaShort := calculateSMA(prices, shortPeriod)
	emaLong := calculateSMA(prices, longPeriod)

	for i := 0; i < len(prices); i++ {
		macd[i] = emaShort[i] - emaLong[i]
	}
	signal = calculateSMA(macd, signalPeriod)
	return macd, signal
}

// **計算 Bollinger Bands**  中軌 = N 日 SMA（簡單移動平均線
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
		upperBand[i] = sma[i] + 2*stdDev //上軌 = 中軌 + 2 × 標準差
		lowerBand[i] = sma[i] - 2*stdDev //下軌 = 中軌 - 2 × 標準差
	}

	return upperBand, lowerBand
}

// **計算 Momentum（動量指標） 指標**
func calculateMomentum(prices []float64, period int) []float64 {
	momentum := make([]float64, len(prices)) 

	for i := period; i < len(prices); i++ {
		momentum[i] = prices[i] - prices[i-period] // Momentum = 當前價格 - n 天前價格
	}

	return momentum
}

// **計算 Chip Ratio（籌碼集中度）**
func calculateChipRatio(prices []float64, period int) []float64 {
	chipRatio := make([]float64, len(prices))

	for i := period; i < len(prices); i++ {
		sum := 0.0
		for j := i - period + 1; j <= i; j++ {
			sum += prices[j]
		}
		average := sum / float64(period)
		chipRatio[i] = prices[i] / average // 當前價格 / 過去 n 天均價
	}

	return chipRatio
}

// **計算 KD 指標**
func calculateKD(prices []float64, period int) ([]float64, []float64) {
	k := make([]float64, len(prices))
	d := make([]float64, len(prices))

	for i := period - 1; i < len(prices); i++ {
		low := prices[i]
		high := prices[i]
		// 找出區間內的最高價與最低價
		for j := i - period + 1; j <= i; j++ {
			if prices[j] < low {
				low = prices[j]
			}
			if prices[j] > high {
				high = prices[j]
			}
		}

		// 計算 RSV（未成熟隨機值）
		if high != low {
			rsv := (prices[i] - low) / (high - low) * 100
			if i == period-1 {
				k[i] = 50 // K 值初始設定為 50
				d[i] = 50 // D 值初始設定為 50
			} else {
				k[i] = (2*k[i-1] + rsv) / 3 // K值計算公式
				d[i] = (2*d[i-1] + k[i]) / 3 // D值計算公式
			}
		} else {
			k[i] = k[i-1] // 若高低相等，K 值不變
			d[i] = d[i-1] // D 值不變
		}
	}

	return k, d
}

// **回測邏輯**
// 每次買入與賣出視為一組交易，輸出交易紀錄
func backtest(dates []string, prices []float64, strategyName string) Performance {
	capital := 1000000.0 // **初始資金 100 萬**
	position := 0.0      // **持倉數量**
	buyPrice := 0.0		 // **記錄買入價格**
	lastBuyDate := ""	 // **記錄上次買入日期**
	var profitHistory []float64
	var wins, losses, trades int

	// **計算技術指標**
	sma5 := calculateSMA(prices, 5)
	sma20 := calculateSMA(prices, 20)
	upperBB, lowerBB := calculateBollingerBands(prices, 20)
	k, d := calculateKD(prices, 9)  
	rsi := calculateRSI(prices, 14)
	momentum := calculateMomentum(prices, 10)
	chipRatio := calculateChipRatio(prices, 10)

	// **MACD 需至少 26 筆資料，其他指標則可在較少數據時計算**
	var macd, signal []float64
	if len(prices) >= 26 {
		macd, signal = calculateMACD(prices, 12, 26, 9)
	}

	fmt.Printf("\n開始回測策略: %s... 初始資金 100 萬\n", strategyName)

	// **從舊到新進行回測，確保買入在賣出之前**
	for i := 0; i < len(prices); i++ {
		shouldBuy := false
		shouldSell := false

		// **確保 MACD 至少有 26 筆資料才計算**
		isMACDReady := i >= 26 

		// **買入條件**
		if position == 0 && capital >= prices[i] && capital >= 10000 {
			switch strategyName {
			case "SMA":// SMA 買入條件：當 5 日均線上穿 20 日均線，代表短期趨勢變強
				if i >= 5 && sma5[i] > sma20[i] && sma5[i-1] <= sma20[i-1] {
					shouldBuy = true
				}
			case "MACD":// MACD 買入條件：當 MACD 線上穿過信號線，代表市場可能進入上升趨勢
				if isMACDReady && macd[i] > signal[i] {
					shouldBuy = true
				}
			case "Bollinger Bands":// Bollinger Bands 買入條件：當價格低於布林通道下軌，代表市場可能超賣
				if i >= 20 && prices[i] < lowerBB[i] {
					shouldBuy = true
				}
			case "KD":// KD 買入條件：當 K 值由下向上穿越 D 值，代表短線可能進入多頭行情
				if i >= 9 && k[i] > d[i] && k[i-1] <= d[i-1] {
					shouldBuy = true
				}
			case "RSI":// RSI 買入條件：當價格低於前一天，代表 RSI 可能進入超賣區
				if i >= 14 && rsi[i] < 30 {
					shouldBuy = true
				}
			case "Momentum":// Momentum 買入條件：當價格高於 5 天前的價格，代表市場趨勢向上
				if i >= 10 && momentum[i] > 0 {
					shouldBuy = true
				}
			case "ChipRatio":// ChipRatio 買入條件：當價格低於 10 天前的價格，代表可能出現籌碼集中
				if i >= 10 && chipRatio[i] > 0.5 {
					shouldBuy = true
				}
			}

			if shouldBuy {
				position = capital / prices[i]
				buyPrice = prices[i]
				capital = 0
				lastBuyDate = dates[i]
				//trades++ 買入不算交易次數，賣出再算
				fmt.Printf("[交易紀錄]\n買入日期: %s  價格: %.2f  持倉數: %.2f  現有資金: %.2f\n", dates[i], prices[i], position, capital)
			}
		}

		// **賣出條件（確保賣出順序正確）**
		if position > 0 && lastBuyDate != "" && dates[i] > lastBuyDate {
			switch strategyName {
			case "SMA":// SMA 賣出條件：當 5 日均線下穿 20 日均線，代表短期趨勢減弱
				if i >= 5 && sma5[i] < sma20[i] && sma5[i-1] >= sma20[i-1] {
					shouldSell = true
				}
			case "MACD":// MACD 賣出條件：當 MACD 線下穿信號線，代表市場可能進入下降趨勢
				if isMACDReady && macd[i] < signal[i] {
					shouldSell = true
				}
			case "Bollinger Bands":// Bollinger Bands 賣出條件：當價格高於布林通道上軌，代表市場可能超買
				if i >= 20 && prices[i] > upperBB[i] {
					shouldSell = true
				}
			case "KD":// KD 賣出條件：當 K 值由上向下跌破 D 值，代表短線可能進入空頭行情
				if i >= 9 && k[i] < d[i] && k[i-1] >= d[i-1] {
					shouldSell = true
				}
			case "RSI":// RSI 賣出條件：當價格高於前一天，代表 RSI 可能進入超買區
				if i >= 14 && rsi[i] > 70 {
					shouldSell = true
				}
			case "Momentum":// Momentum 賣出條件：當價格低於 5 天前的價格，代表市場趨勢轉弱
				if i >= 10 && momentum[i] < 0 {
					shouldSell = true
				}
			case "ChipRatio":// ChipRatio 賣出條件：當價格高於 10 天前的價格，代表籌碼可能鬆動
				if i >= 10 && chipRatio[i] < 0.5 {
					shouldSell = true
				}
			}
			// **確保賣出日期比買入日期晚**
			if shouldSell {
				sellAmount := position * prices[i]// 計算賣出金額
				profit := sellAmount - (position * buyPrice)
				capital = sellAmount  // **賣出後，資金回復，可用於下一次交易**
				fmt.Printf("賣出日期: %s  價格: %.2f  獲利: %.2f  現有資金: %.2f\n", dates[i], prices[i], profit, capital)
				fmt.Println("---------------------------------------------")
				position = 0  // **賣出後，清空持倉**
				lastBuyDate = "" // 重置買入日期，確保下一次交易能夠正常執行
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

	// **若最後仍有持倉，則以最後一天價格計算總資產**
	if position > 0 {
		capital = position * prices[len(prices)-1] // 假設最後一天以當前價格估算資產
		a := prices[len(prices)-1]
		fmt.Printf("尚未賣出，最後股價: %.2f\n", a)
		fmt.Printf("尚未賣出，當前資產總額: %.2f\n", capital)
		position = 0
	}

	if len(profitHistory) == 0 {
		profitHistory = append(profitHistory, capital) // 若無交易，回測仍需有數據
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
	dates, prices, err := readCSV("2330_stock_data.csv")//選取資料來源
	if err != nil {
		fmt.Println("讀取 CSV 失敗:", err)
		return
	}

	// 執行各策略回測，這裡以 RSI、MACD、Bollinger Bands 為例，其它策略可依需求添加
	rsiPerf := backtest(dates, prices, "RSI")
	kdPerf := backtest(dates, prices, "KD")
	macdPerf := backtest(dates, prices, "MACD")
	smaPerf := backtest(dates, prices, "SMA")
	momentumPerf := backtest(dates, prices, "Momentum")
	chipratioPerf := backtest(dates, prices, "ChipRatio")
	bollingerPerf := backtest(dates, prices, "Bollinger Bands")
	// 其它策略例如 SMA、Momentum、ChipRatio、KD 可依需求添加

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


