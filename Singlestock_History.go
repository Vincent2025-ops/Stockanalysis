package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"time"
	"strings"
	"strconv"
)

// TWSE API 回應的結構
type TwseResponse struct {
	Fields []string   `json:"fields"` // 欄位標題
	Data   [][]string `json:"data"`   // 股票數據
	Stat   string     `json:"stat"`   // API 回應狀態
}

// 取得指定月份的股票數據
func fetchStockData(stockNo, date string) ([][]string, error) {
	url := fmt.Sprintf("https://www.twse.com.tw/exchangeReport/STOCK_DAY?response=json&date=%s&stockNo=%s", date, stockNo)

	// 發送 HTTP GET 請求
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("無法連接 TWSE API: %v", err)
	}
	defer resp.Body.Close()

	// 讀取 API 回應
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("讀取回應失敗: %v", err)
	}

	// 解析 JSON
	var result TwseResponse
	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, fmt.Errorf("解析 JSON 失敗: %v", err)
	}

	// 確保 API 回傳狀態為 "OK"
	if result.Stat != "OK" {
		return nil, fmt.Errorf("API 回傳錯誤: %s", result.Stat)
	}

	return result.Data, nil
}


// 🛠 **將民國日期轉換為西元 YYYYMMDD**
func convertToAD(twDate string) string {
	// 假設輸入格式為 "113/03/11"
	parts := strings.Split(twDate, "/")
	if len(parts) != 3 {
		return "00000000" // 若格式錯誤，避免影響排序
	}

	// 轉換民國年為西元年
	year, _ := strconv.Atoi(parts[0])
	year += 1911
	month := parts[1]
	day := parts[2]

	return fmt.Sprintf("%04d%02s%02s", year, month, day) // 生成 YYYYMMDD 格式
}


func main() {
	stockNo := "2330"  // 股票代碼
	numMonths := 12    // 回測的月份數為 12 個月，確保數據足夠
	currentTime := time.Now()

	// 取得 CSV 檔案
	file, err := os.Create(fmt.Sprintf("%s_stock_data.csv", stockNo))
	if err != nil {
		fmt.Println("無法建立 CSV 檔案:", err)
		return
	}
	defer file.Close()

	// **加入 UTF-8 BOM**
	file.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// 取得最新 12 個月的歷史股價
	fmt.Println("開始下載過去 12 個月的歷史股價...")
	var allData [][]string

	for i := 0; i < numMonths; i++ {
		// 計算對應月份（往前推 i 個月）
		monthTime := currentTime.AddDate(0, -i, 0)
		dateStr := monthTime.Format("20060102") // 格式: YYYYMMDD (API 會回傳整個月)

		fmt.Printf("下載 %s 月的數據...\n", monthTime.Format("2006-01"))

		// 抓取該月份的股價數據
		monthlyData, err := fetchStockData(stockNo, dateStr)
		if err != nil {
			fmt.Println("錯誤:", err)
			continue
		}

		// 累積所有數據
		allData = append(allData, monthlyData...)
	}

	// ✅ **按照日期升冪排序**
	sort.Slice(allData, func(i, j int) bool {
		// 假設日期在欄位 [0]，格式為 "113/03/11"（民國年）
		// 轉換為西元年 YYYYMMDD 以正確排序
		dateI := convertToAD(allData[i][0])
		dateJ := convertToAD(allData[j][0])
		return dateI < dateJ // 升冪排序（最新日期在最上方）
	})
	

	// ✅ **寫入標題**
	writer.Write([]string{"日期", "成交股數", "成交金額", "開盤價", "最高價", "最低價", "收盤價", "漲跌價差", "成交筆數"})

	// ✅ **寫入排序後的數據**
	writer.WriteAll(allData)

	fmt.Printf("📂 股票數據已儲存至 %s_stock_data.csv（日期已排序）\n", stockNo)
}
