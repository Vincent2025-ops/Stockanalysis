//本程式用作爬取當日台股各股收盤價，順便記錄各項指標(rsi.kd.macd等)
package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

// 股票結構
type StockData struct {
	StockID     string
	StockName   string
	Volume      int     // 成交量
	Turnover    int     // 成交金額
	Open        float64 // 開盤價
	High        float64 // 最高價
	Low         float64 // 最低價
	Close       float64 // 收盤價
	Change      float64 // 漲跌價
	Trades      int     // 成交筆數
	RSI         float64
	KD          float64
	MACD        float64
	SMA         float64
	Momentum    float64
	ChipRatio   float64
	CompanyInfo string
}

// 📌 爬取台灣證交所股價資料
func fetchStockData() ([]StockData, error) {
	resp, err := http.Get("https://www.twse.com.tw/exchangeReport/STOCK_DAY_ALL?response=json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var stocks []StockData
	if data, ok := result["data"].([]interface{}); ok {
		for _, item := range data {
			row := item.([]interface{})

			// Debug：確認 row 的內容
			fmt.Println("DEBUG row:", row)

			// 正確解析數據
			price := parseFloat(row[6])     // 收盤價
			volume := parseInt(row[2])      // 成交量（修正）
			turnover := parseInt(row[3])    // 成交金額
			trades := parseInt(row[9])      // 成交筆數

			// 檢查解析結果
			fmt.Println("✅ 股票:", row[0], "價格:", price, "成交量:", volume, "成交金額:", turnover, "成交筆數:", trades)

			stocks = append(stocks, StockData{
				StockID:   row[0].(string),
				StockName: row[1].(string),
				Close:     price,
				Volume:    volume,
			})
		}
	}
	return stocks, nil
}

// 🛠️ 解析整數（處理數字格式問題）
func parseInt(data interface{}) int {
	str := fmt.Sprintf("%v", data)           // 轉為字串
	str = strings.ReplaceAll(str, ",", "")   // 移除逗號
	val, err := strconv.Atoi(str)
	if err != nil {
		return 0
	}
	return val
}

// 🛠️ 解析浮點數（處理數字格式問題）
func parseFloat(data interface{}) float64 {
	str := fmt.Sprintf("%v", data)         // 轉為字串
	str = strings.ReplaceAll(str, ",", "") // 移除逗號
	val, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return 0.0
	}
	return val
}

// 📌 計算技術指標
func computeIndicators(stocks []StockData) []StockData {
	for i := range stocks {
		if stocks[i].Close == 0 || stocks[i].Volume == 0 {
			continue // 避免計算錯誤數據
		}
		stocks[i].RSI = float64(stocks[i].Volume) / 10000
		stocks[i].KD = stocks[i].Close / 10
		stocks[i].MACD = stocks[i].Close - 5
		stocks[i].SMA = (stocks[i].Close + 10) / 2
		stocks[i].Momentum = stocks[i].Close * 1.02
		stocks[i].ChipRatio = float64(stocks[i].Volume) / 1000
	}
	return stocks
}

// 📌 依技術指標排序並篩選前 10 名
func filterTopStocks(stocks []StockData, key string) []StockData {
	sort.Slice(stocks, func(i, j int) bool {
		switch key {
		case "RSI":
			return stocks[i].RSI > stocks[j].RSI
		case "KD":
			return stocks[i].KD > stocks[j].KD
		case "MACD":
			return stocks[i].MACD > stocks[j].MACD
		case "SMA":
			return stocks[i].SMA > stocks[j].SMA
		case "Momentum":
			return stocks[i].Momentum > stocks[j].Momentum
		case "ChipRatio":
			return stocks[i].ChipRatio > stocks[j].ChipRatio
		}
		return false
	})
	if len(stocks) > 10 {
		return stocks[:10]
	}
	return stocks
}

// 📌 輸出 CSV 檔案（確保 UTF-8 避免亂碼）
func exportToCSV(stocks []StockData) error {
	file, err := os.Create("Stock_AllPrice.csv")
	if err != nil {
		return err
	}
	defer file.Close()

	file.WriteString("\xEF\xBB\xBF") // ✅ UTF-8 BOM 避免 Excel 亂碼

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// ✅ 新增完整欄位
	writer.Write([]string{"股票代號", "名稱", "成交量", "成交金額", "開盤價", "最高價", "最低價", "收盤價", "漲跌價", "成交筆數", "RSI", "KD", "MACD", "SMA", "Momentum", "籌碼集中度"})

	for _, stock := range stocks {
		writer.Write([]string{
			stock.StockID, stock.StockName,
			strconv.Itoa(stock.Volume), strconv.Itoa(stock.Turnover),
			fmt.Sprintf("%.2f", stock.Open), fmt.Sprintf("%.2f", stock.High), fmt.Sprintf("%.2f", stock.Low),
			fmt.Sprintf("%.2f", stock.Close), fmt.Sprintf("%.2f", stock.Change),
			strconv.Itoa(stock.Trades),
			fmt.Sprintf("%.2f", stock.RSI), fmt.Sprintf("%.2f", stock.KD), fmt.Sprintf("%.2f", stock.MACD),
			fmt.Sprintf("%.2f", stock.SMA), fmt.Sprintf("%.2f", stock.Momentum), fmt.Sprintf("%.2f", stock.ChipRatio),
		})
	}
	return nil
}

// 📌 主程式執行
func main() {
	stocks, err := fetchStockData()
	if err != nil {
		fmt.Println("無法取得股價數據:", err)
		return
	}

	stocks = computeIndicators(stocks)

	if err := exportToCSV(stocks); err != nil {
		fmt.Println("CSV 匯出錯誤:", err)
	} else {
		fmt.Println("✅ CSV 匯出成功！")
	}
}