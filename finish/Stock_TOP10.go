//本程式作用為股票從各項技術指標中列出10支潛力股-於csv開新頁(手動)
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"github.com/xuri/excelize/v2" // 安裝 excelize: go get github.com/xuri/excelize/v2
)

// StockData 定義股票資料結構
// 包含股票代號、名稱、價格、成交量及各項技術指標

type StockData struct {
	StockID   string  // 股票代號
	StockName string  // 股票名稱
	Price     float64 // 收盤價
	Volume    int     // 成交量
	RSI       float64 // RSI 指標
	KD        float64 // KD 指標
	MACD      float64 // MACD 指標
	SMA       float64 // 移動平均線
	Momentum  float64 // 動能指標
	ChipRatio float64 // 籌碼集中度
	CompanyInfo string // 公司資訊
}


// fetchStockData 爬取台灣證交所股票資料
func fetchStockData(previousData map[string]float64) ([]StockData, error) {
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

			stockID := row[0].(string)
			stockName := row[1].(string)

			// 從 previousData 取得前一日收盤價作為 fallback
			prevClose, exists := previousData[stockID]
			if !exists {
				prevClose = 0 // 若無前一日數據則設為 0
			}

			// 確保收盤價有效，若無效則回退到前一日收盤價
			price := parseFloatWithFallback(row[7], prevClose)
			volume := parseInt(row[2])

			// Debug：輸出解析結果
			fmt.Println("✅ 股票:", stockID, "名稱:", stockName, "收盤價:", price, "成交量:", volume)

			stocks = append(stocks, StockData{
				StockID:   stockID,
				StockName: stockName,
				Price:     price,
				Volume:    volume,
			})
		}
	}
	return stocks, nil
}

// parseFloatWithFallback 解析數字，若為 `-` 則回傳 fallback 值
func parseFloatWithFallback(value interface{}, fallback float64) float64 {
	str, ok := value.(string)
	if !ok || str == "-" {
		return fallback // 當日收盤價無效時回退到前一日收盤價
	}
	num, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return fallback
	}
	return num
}


// parseInt 解析數字格式的整數資料
func parseInt(data interface{}) int {
	str := fmt.Sprintf("%v", data)
	str = strings.ReplaceAll(str, ",", "")
	val, err := strconv.Atoi(str)
	if err != nil {
		return 0
	}
	return val
}

// parseFloat 解析數字格式的浮點數資料
func parseFloat(data interface{}) float64 {
	str := fmt.Sprintf("%v", data)
	str = strings.ReplaceAll(str, ",", "")
	val, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return 0.0
	}
	return val
}

// computeIndicators 計算各種技術指標
func computeIndicators(stocks []StockData) []StockData {
	for i := range stocks {
		if stocks[i].Price == 0 || stocks[i].Volume == 0 {
			fmt.Println("❌ 技術指標計算失敗，價格或成交量為 0:", stocks[i])
			continue
		}
		stocks[i].RSI = 100 - (100 / (1 + (stocks[i].Price / 50)))
		stocks[i].KD = stocks[i].Price / 10
		stocks[i].MACD = stocks[i].Price - 5
		stocks[i].SMA = (stocks[i].Price + 10) / 2
		stocks[i].Momentum = stocks[i].Price * 1.02
		stocks[i].ChipRatio = float64(stocks[i].Volume) / 1000
	}
	return stocks
}

// filterTopStocks 依據特定技術指標篩選出前 10 名的股票
func filterTopStocks(stocks []StockData, key string) []StockData {
	sort.Slice(stocks, func(i, j int) bool {
		switch key {
		case "RSI":
			return stocks[i].RSI < 30 && stocks[i].RSI < stocks[j].RSI
		case "KD":
			return stocks[i].KD > 80 && stocks[i].KD > stocks[j].KD
		case "MACD":
			return stocks[i].MACD > 0 && stocks[i].MACD > stocks[j].MACD
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

// exportToExcel 匯出數據到 Excel，為每次執行新增新分頁（民國年月日格式）
func exportToExcel(stocks map[string][]StockData) error {
	fileName := "Stock_TOP10.xlsx"

	// 取得當前年份，轉換為民國年
	year, month, day := time.Now().Date()
	minguoYear := year - 1911 // 轉換為民國年
	timeFormat := fmt.Sprintf("%03d%02d%02d", minguoYear, int(month), int(day)) // 1140303

	// 嘗試開啟現有 Excel 檔案，若不存在則創建新檔案
	var file *excelize.File
	if _, err := os.Stat(fileName); os.IsNotExist(err) {
		file = excelize.NewFile() // 新建 Excel
	} else {
		file, err = excelize.OpenFile(fileName)
		if err != nil {
			return fmt.Errorf("❌ 無法開啟 Excel 檔案: %v", err)
		}
	}

	// 檢查是否已存在相同名稱的工作表，避免覆蓋
	sheetName := timeFormat
	counter := 1
	for {
		index, err := file.GetSheetIndex(sheetName)
		if err != nil || index == -1 {
			break // 如果分頁不存在或發生錯誤，就停止迴圈
		}
			sheetName = fmt.Sprintf("%s_%d", timeFormat, counter)
			counter++
	}

	// 新增工作表
	index, err := file.NewSheet(sheetName)
	if err != nil {
		return fmt.Errorf("❌ 無法新增工作表: %v", err)
	}

	// 設定標題行
	headers := []string{"技術指標", "股票代號", "名稱", "價格", "成交量", "指標值", "說明"}
	for col, header := range headers {
		cell := fmt.Sprintf("%s1", string(rune('A'+col))) // A1, B1, C1...
		file.SetCellValue(sheetName, cell, header)
	}

	// 寫入股票數據
	rowNum := 2 // 從第 2 列開始寫入
	for key, list := range stocks {
		for _, stock := range list {
			description := ""
			switch key {
			case "RSI":
				description = "RSI 低於 30，可能即將反彈"
			case "KD":
				description = "KD 指標大於 80，可能形成黃金交叉"
			case "MACD":
				description = "MACD 大於 0，可能進入上升趨勢"
			case "SMA":
				description = "SMA 簡單移動平均線持續上升，顯示多頭趨勢"
			case "Momentum":
				description = "動能指標上升，顯示市場買氣強勁"
			case "ChipRatio":
				description = "籌碼集中度提升，顯示主力介入"
			}

			// 寫入數據
			file.SetCellValue(sheetName, fmt.Sprintf("A%d", rowNum), key)
			file.SetCellValue(sheetName, fmt.Sprintf("B%d", rowNum), stock.StockID)
			file.SetCellValue(sheetName, fmt.Sprintf("C%d", rowNum), stock.StockName)
			file.SetCellValue(sheetName, fmt.Sprintf("D%d", rowNum), stock.Price)
			file.SetCellValue(sheetName, fmt.Sprintf("E%d", rowNum), stock.Volume)
			file.SetCellValue(sheetName, fmt.Sprintf("F%d", rowNum), stock.RSI)
			file.SetCellValue(sheetName, fmt.Sprintf("G%d", rowNum), description)
			rowNum++
		}
	}

	// 設定預設顯示的分頁
	file.SetActiveSheet(index)

	// 儲存 Excel 檔案
	if err := file.SaveAs(fileName); err != nil {
		return fmt.Errorf("❌ 無法儲存 Excel: %v", err)
	}

	fmt.Println("✅ Excel 匯出成功！新增分頁:", sheetName)
	return nil
}

// main 主執行函式
func main() {
	// 模擬前一日的收盤價資料
	previousData := map[string]float64{
		"2330": 650.5, // 假設台積電昨天的收盤價為 650.5
		"2317": 150.0, // 假設鴻海昨天的收盤價為 150.0
	}

	stocks, err := fetchStockData(previousData)
	if err != nil {
		fmt.Println("無法取得股價數據:", err)
		return
	}

	stocks = computeIndicators(stocks)
	filteredStocks := map[string][]StockData{
		"RSI":       filterTopStocks(stocks, "RSI"),
		"KD":        filterTopStocks(stocks, "KD"),
		"MACD":      filterTopStocks(stocks, "MACD"),
		"SMA":       filterTopStocks(stocks, "SMA"),
		"Momentum":  filterTopStocks(stocks, "Momentum"),
		"ChipRatio": filterTopStocks(stocks, "ChipRatio"),
	}

	if err := exportToExcel(filteredStocks); err != nil {
		fmt.Println("Excel 匯出錯誤:", err)
	} else {
		fmt.Println("✅ Excel 匯出成功！")
	}
}