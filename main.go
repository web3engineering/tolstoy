package main

import (
	"database/sql"
	"fmt"
	"os"
	"log"
	"net/http"
	"encoding/json"
		"math/big"
	"time"
	"context"

	_ "github.com/go-sql-driver/mysql"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
)


func reconnect(rpc_index *int) (bind.ContractBackend) {
	for {
		rpc_env := fmt.Sprintf("RPC_URL_%v", *rpc_index)
		rpc_url := os.Getenv(rpc_env)
		if rpc_url == "" {
			*rpc_index = 1
			continue
		}
		log.Printf("Trying to connect to %v", rpc_url)
		сlient, err := ethclient.Dial(rpc_url)
		if err != nil {
			log.Printf("Failed to connect to blockhain:", err)
			time.Sleep(time.Duration(30)*time.Second)
			*rpc_index = *rpc_index + 1
			continue
		}
		log.Printf("Successfully connected to RPC %v", rpc_url)
		return сlient
	}
}


func runScanner(db *sql.DB) {
	log.Printf("Scanner started")
	rpc_index := 1
	client := reconnect(&rpc_index)

	poolContractAddress := common.HexToAddress(os.Getenv("POOL_CONTRACT"))
	poolContract, err := NewCasinoPool(poolContractAddress, client)
	if err != nil {
		log.Fatal("Failed to create Pool contract binding:", err)
	}

	opts := new(bind.FilterOpts)
	opts.Start = 29192370
	opts.End = new(uint64)
	*opts.End = 29192420

	for {
		log.Printf("Querying blockchain for events in %v:%v range", opts.Start, *opts.End)
		transferIter, err := poolContract.FilterSharePriceChanged(opts)
		if err != nil {
			log.Printf("Failed to query events: %v", err)
			log.Printf("Reconnecting")
			rpc_index = rpc_index + 1
			client = reconnect(&rpc_index)
		}

		for transferIter.Next() {
			event := transferIter.Event

			log.Printf("Found event %v", event)
			log.Printf("Found event %v", event.Raw.BlockNumber)

			_, err = db.Exec("INSERT IGNORE INTO pnl_changes (tx, block_number, stored_eth, total_shares) VALUES (?, ?, ?, ?)",
				event.Raw.TxHash.String(),
				event.Raw.BlockNumber,
				big.NewInt(0).Div(event.NewPriceNom, big.NewInt(0x100000000)).Uint64(),
				big.NewInt(0).Div(event.NewPriceDenom, big.NewInt(0x100000000)).Uint64());

			if err != nil {
				log.Fatal("Unable to insert to DB: %v", err)
			}
		}

		for {
			time.Sleep(time.Duration(5)*time.Second)
			header, err := client.HeaderByNumber(context.Background(), nil)
			if err != nil {
				log.Printf("Failed to get header:", err)
				rpc_index = rpc_index + 1
				client = reconnect(&rpc_index)
			}
			log.Printf("The most recent block is %v", header.Number)
			if header.Number.Uint64() < *opts.End + 50 {
				time.Sleep(time.Duration(120)*time.Second)
			} else {
				break
			}
		}
		opts.Start = *opts.End
		*opts.End = *opts.End + 50
	}

	log.Printf("No new events")
}

func ensureDatabases(db *sql.DB) {
	log.Printf("Creating table")
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS pnl_changes(
		tx CHAR(100) PRIMARY KEY,
		block_number BIGINT NOT NULL,
		stored_eth BIGINT NOT NULL,
		total_shares BIGINT NOT NULL
	)`)
	if err != nil {
		log.Fatal("Failed to create table:", err)
	}
}


func main() {
	passwd := os.Getenv("MYSQL_ROOT_PASSWORD")
	dsn := fmt.Sprintf("root:%v@tcp(db:3306)/pnl", passwd)
	log.Printf("Connecting to %v", dsn)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal("Unable to connect to mysql", err)
	}
	db.SetConnMaxLifetime(0)
	db.SetMaxIdleConns(50)
	db.SetMaxOpenConns(50)

	ensureDatabases(db)

	s := &Service{db: db}
	go runScanner(db)
	http.ListenAndServe(":8080", s)
}

type Service struct {
	db *sql.DB
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	db := s.db
	type PnlEntry = struct {
		Block uint64 `json:"block"`
		SharePrice float64 `json:"share_price"`
	}
	type Response = struct {
		Data []PnlEntry `json:"data"`
	};
	var resp Response

	rows, err := db.Query("select block_number, CAST(stored_eth as FLOAT)/CAST(total_shares as FLOAT) from pnl_changes;")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for rows.Next() {
		var entry PnlEntry
		err = rows.Scan(&entry.Block, &entry.SharePrice)
		if err != nil {
			break
		}
		resp.Data = append(resp.Data, entry)
	}
	// Check for errors during rows "Close".
	// This may be more important if multiple statements are executed
	// in a single batch and rows were written as well as read.
	if closeErr := rows.Close(); closeErr != nil {
		http.Error(w, closeErr.Error(), http.StatusInternalServerError)
		return
	}

	// Check for row scan error.
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Check for errors during row iteration.
	if err = rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	json.NewEncoder(w).Encode(resp)
	return
}