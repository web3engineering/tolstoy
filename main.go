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
	"sync/atomic"
	"strconv"

	_ "github.com/go-sql-driver/mysql"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
)

var QUERY_RANGE uint64 = 50
var SCAN_DELAY uint64 = 20  // To deal with reords in a lazy manner we are just not fetching 20 latest blocks

var reconnect_attempts_metrics uint32 = 0
var last_block_metrics uint64 = 0


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
			atomic.AddUint32(&reconnect_attempts_metrics, 1)
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

	slotsContractAddress := common.HexToAddress(os.Getenv("SLOTS_CONTRACT"))
	slotsContract, err := NewSlots(slotsContractAddress, client)
	if err != nil {
		log.Fatal("Failed to create slots contract binding:", err)
	}

	rouletteContractAddress := common.HexToAddress(os.Getenv("ROULETTE_CONTRACT"))
	rouletteContract, err := NewSlots(rouletteContractAddress, client)
	if err != nil {
		log.Fatal("Failed to create roulette contract binding:", err)
	}

	coinflipContractAddress := common.HexToAddress(os.Getenv("COINFLIP_CONTRACT"))
	coinflipContract, err := NewCoinflip(coinflipContractAddress, client)
	if err != nil {
		log.Fatal("Failed to create coinflip contract binding:", err)
	}

	slotsGameResEventSignature := []byte("GameResEvent(uint256,address,uint256,uint256,uint256,uint256)")
	slotsGameResEventSignatureHash := crypto.Keccak256Hash(slotsGameResEventSignature)
    log.Printf("slotsGameResEventSignatureHash %v", slotsGameResEventSignatureHash.Hex())

	rouletteGameResEventSignature := []byte("GameResEvent(uint256,address,uint256,uint256)");
	rouletteGameResEventSignatureHash := crypto.Keccak256Hash(rouletteGameResEventSignature)
	log.Printf("rouletteGameResEventSignatureHash %v", rouletteGameResEventSignatureHash.Hex())

	coinflipGameResEventSignature := []byte("GameResEvent(uint256,address,uint256,uint256)");
	coinflipGameResEventSignatureHash := crypto.Keccak256Hash(coinflipGameResEventSignature)
	log.Printf("coinflipGameResEventSignatureHash %v", coinflipGameResEventSignatureHash.Hex())

	poolSharePriceChangedEventSiganture := []byte("SharePriceChanged(uint256,uint256)")
	poolSharePriceChangedEventSigantureHash := crypto.Keccak256Hash(poolSharePriceChangedEventSiganture)
	log.Printf("poolSharePriceChangedEventSigantureHash %v", poolSharePriceChangedEventSigantureHash.Hex())

	poolReferralPaymentSiganture := []byte("ReferralPayment(address,uint256)")
	poolReferralPaymentSigantureHash := crypto.Keccak256Hash(poolReferralPaymentSiganture)
	log.Printf("poolReferralPaymentSigantureHash %v", poolReferralPaymentSigantureHash.Hex())

	var last_block_from_db uint64 = 0
	log.Printf("Getting last block from DB...")
	err = db.QueryRow("SELECT last_block FROM system_status WHERE r=1").Scan(&last_block_from_db)
	if err != nil {
		log.Printf("ERROR: unable to get last block from DB %v", err)
	}
	if last_block_from_db == 0 {
		log.Fatal("ERROR: last block from DB is missing")
	}

	opts := new(bind.FilterOpts)
	opts.Start = last_block_from_db
	opts.End = new(uint64)
	*opts.End = last_block_from_db + QUERY_RANGE

	for {
		log.Printf("Querying blockchain for events in %v:%v range", opts.Start, *opts.End)

		query := ethereum.FilterQuery{
			FromBlock: big.NewInt(int64(opts.Start)),
			ToBlock:   big.NewInt(int64(*opts.End)),
			Addresses: []common.Address{
				poolContractAddress,
				slotsContractAddress,
				rouletteContractAddress,
				coinflipContractAddress,
			},
			Topics: [][]common.Hash{{
				poolSharePriceChangedEventSigantureHash,
				poolReferralPaymentSigantureHash,
				slotsGameResEventSignatureHash,
				rouletteGameResEventSignatureHash,
				coinflipGameResEventSignatureHash,
			}},
		}

		rawLogs, err := client.FilterLogs(context.Background(), query)
		if err != nil {
			log.Printf("Failed to query events: %v", err)
			log.Printf("Reconnecting")
			rpc_index = rpc_index + 1
			client = reconnect(&rpc_index)
			continue
		}

		log.Printf("Query=%v", query, "Logs=%v", rawLogs)

		for _, rawLog := range(rawLogs) {
			if rawLog.Address == slotsContractAddress && rawLog.Topics[0] == slotsGameResEventSignatureHash {
				slotGameResLog, err := slotsContract.ParseGameResEvent(rawLog)
				if err != nil {
					log.Fatalf("Unable to parse slots::GameResEvent %v", err)
				}
		
				log.Printf("Found Slots Game Res %v", rawLog)
				_, err = db.Exec("INSERT IGNORE INTO recent_games(tx, block_number, game_type, player, amount) VALUES (?, ?, 'SLOT', ?, ?)",
					fmt.Sprintf("%s:%d", rawLog.TxHash.String(), rawLog.Index),
					rawLog.BlockNumber,
					slotGameResLog.UserAddress.Hex(),
					big.NewInt(0).Div(slotGameResLog.WinAmount, big.NewInt(1000000000)).Uint64());
	
				if err != nil {
					log.Fatal("Unable to insert to DB: %v", err)
				}
				continue
			}

			if rawLog.Address == rouletteContractAddress && rawLog.Topics[0] == rouletteGameResEventSignatureHash {
				rouletteGameResLog, err := rouletteContract.ParseGameResEvent(rawLog)
				if err != nil {
					log.Fatalf("Unable to parse roulette::GameResEvent %v", err)
				}

				log.Printf("Found Roulette Game Res %v", rawLog)
				_, err = db.Exec("INSERT IGNORE INTO recent_games(tx, block_number, game_type, player, amount) VALUES (?, ?, 'ROULETTE', ?, ?)",
					fmt.Sprintf("%s:%d", rawLog.TxHash.String(), rawLog.Index),
					rawLog.BlockNumber,
					rouletteGameResLog.UserAddress.Hex(),
					big.NewInt(0).Div(rouletteGameResLog.WinAmount, big.NewInt(1000000000)).Uint64());
	
				if err != nil {
					log.Fatal("Unable to insert to DB: %v", err)
				}
				continue
			}

			if rawLog.Address == coinflipContractAddress && rawLog.Topics[0] == coinflipGameResEventSignatureHash {
				coinflipGameResLog, err := coinflipContract.ParseGameResEvent(rawLog)
				if err != nil {
					log.Fatalf("Unable to parse coinflip::GameResEvent %v", err)
				}
		
				log.Printf("Found Coinflip Game Res %v", rawLog)
				_, err = db.Exec("INSERT IGNORE INTO recent_games(tx, block_number, game_type, player, amount) VALUES (?, ?, 'COINFLIP', ?, ?)",
					fmt.Sprintf("%s:%d", rawLog.TxHash.String(), rawLog.Index),
					rawLog.BlockNumber,
					coinflipGameResLog.UserAddress.Hex(),
					big.NewInt(0).Div(coinflipGameResLog.WinAmount, big.NewInt(1000000000)).Uint64());
	
				if err != nil {
					log.Fatal("Unable to insert to DB: %v", err)
				}
				continue
			}

			if rawLog.Address == poolContractAddress && rawLog.Topics[0] == poolSharePriceChangedEventSigantureHash {
				poolContractSharePriceChangedResLog, err := poolContract.ParseSharePriceChanged(rawLog)
				if err != nil {
					log.Fatalf("Unable to parse poolContract::SharePriceChanged %v", err)
				}
		
				log.Printf("Found PnL change event %v", rawLog)
				_, err = db.Exec("INSERT IGNORE INTO pnl_changes (tx, block_number, stored_eth, total_shares) VALUES (?, ?, ?, ?)",
					rawLog.TxHash.String(),
					rawLog.BlockNumber,
					big.NewInt(0).Div(poolContractSharePriceChangedResLog.NewPriceNom, big.NewInt(1000000000)).Uint64(),
					big.NewInt(0).Div(poolContractSharePriceChangedResLog.NewPriceDenom, big.NewInt(1000000000)).Uint64());

				if err != nil {
					log.Fatal("Unable to insert to DB: %v", err)
				}
				continue
			}

			if rawLog.Address == poolContractAddress && rawLog.Topics[0] == poolReferralPaymentSigantureHash {
				poolContractReferralPaymentLog, err := poolContract.ParseReferralPayment(rawLog)
				if err != nil {
					log.Fatalf("Unable to parse poolContract::ReferralPayment %v", err)
				}
		
				log.Printf("Found Ref Payment Event %v", rawLog)
				_, err = db.Exec("INSERT IGNORE INTO ref_payments (tx, block_number, ref, payment_received) VALUES (?, ?, ?, ?)",
					fmt.Sprintf("%s:%d", rawLog.TxHash.String(), rawLog.Index),
					rawLog.BlockNumber,
					poolContractReferralPaymentLog.Referrer.Hex(),
					big.NewInt(0).Div(poolContractReferralPaymentLog.ReferralPaymentAmount, big.NewInt(1000000000)).Uint64());

				if err != nil {
					log.Fatal("Unable to insert to DB: %v", err)
				}
				continue
			}
		}
		
		atomic.StoreUint64(&last_block_metrics, *opts.End)
		_, err = db.Exec("UPDATE system_status SET last_block = ? WHERE r=1 AND last_block < ?", last_block_metrics, last_block_metrics)
		if err != nil {
			log.Fatal("Unable to update system_status DB: %v", err)
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
			if header.Number.Uint64() < *opts.End + QUERY_RANGE + SCAN_DELAY {
				time.Sleep(time.Duration(120)*time.Second)
			} else {
				break
			}
		}
		opts.Start = *opts.End
		*opts.End = *opts.End + QUERY_RANGE
	}

	log.Printf("No new events")
}

func ensureDatabases(db *sql.DB) {
	log.Printf("Creating table pnl_changes")
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS pnl_changes(
		tx CHAR(100) PRIMARY KEY,
		block_number BIGINT NOT NULL,
		stored_eth BIGINT NOT NULL,
		total_shares BIGINT NOT NULL
	)`)
	if err != nil {
		log.Fatal("Failed to create table:", err)
	}

	log.Printf("Creating table ref_payments")
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS ref_payments(
		tx CHAR(100) PRIMARY KEY,
		block_number BIGINT NOT NULL,
		ref CHAR(42) NOT NULL,
		payment_received BIGINT NOT NULL
	)`)
	if err != nil {
		log.Fatal("Failed to create table:", err)
	}
	log.Printf("Creating table recent_games")
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS recent_games(
		tx CHAR(100) PRIMARY KEY,
		block_number BIGINT NOT NULL,
		game_type CHAR(10) NOT NULL,
		player CHAR(42) NOT NULL,
		amount BIGINT NOT NULL
	)`)
	if err != nil {
		log.Fatal("Failed to create table:", err)
	}

	log.Printf("Creating table system_status")
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS system_status(
		last_block BIGINT NOT NULL
	)`)
	if err != nil {
		log.Fatal("Failed to create table:", err)
	}

	var system_status_has_rowid uint32 = 0
	err = db.QueryRow("SELECT count(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = 'pnl' AND TABLE_NAME = 'system_status' AND COLUMN_NAME = 'r'").Scan(&system_status_has_rowid)
	if err != nil {
		log.Fatalf("Unable to check if column 'r' exists for system_status migration: %v", err)
	}
	if system_status_has_rowid == 0 {
		log.Printf("Migration for system_status: add column 'r'")
		_, err = db.Exec(`ALTER TABLE system_status ADD COLUMN r INT NOT NULL UNIQUE`)
		if err != nil {
			log.Fatal("Failed to Migrate table:", err)
		}
	} else {
		log.Print("Already Done: Migration for system_status: add column 'r'")
	}

	last_block_from_env, err := strconv.ParseUint(os.Getenv("FIRST_BLOCK"), 10, 64)
	if err != nil {
		log.Fatal("FIRST_BLOCK env variable is missing!")
	}
	_, err = db.Exec("INSERT IGNORE INTO system_status(last_block, r) VALUES (?, 1)", last_block_from_env)
	if err != nil {
		log.Fatalf("Unable to write last_block info system_status: %v", err)
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
	switch r.URL.Path {
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return

	case "/pnl":
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
		defer rows.Close()

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

	case "/referral-payments":
		type ReferralPaymentEntry = struct {
			Address string `json:"address"`
			Amount float64 `json:"amount"`
		}
		type Response = struct {
			Data []ReferralPaymentEntry `json:"data"`
		};
		var resp Response
		var rows *sql.Rows
		var err error

		referral, hasReferral := r.URL.Query()["referral"]
		if !hasReferral {
			rows, err = db.Query("select ref, SUM(payment_received) as amount from ref_payments group by ref;")
		} else {
			rows, err = db.Query("select ref, SUM(payment_received) as amount from ref_payments where LOWER(ref) = LOWER(?) group by ref", referral[0])
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var entry ReferralPaymentEntry
			var amount uint64
			err := rows.Scan(&entry.Address, &amount)
			entry.Amount = float64(amount) / 1e9
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

	case "/recent-games":
		type RecentGameEntry = struct {
			Address string `json:"address"`
			BlockNumber uint64 `json:"block_number"`
			GameType string `json:"game_type"`
			WinAmount float64 `json:"win_amount"`
		}
		type Response = struct {
			Data []RecentGameEntry `json:"data"`
		};
		var resp Response
		var rows *sql.Rows
		var err error

		gameType, hasGameType := r.URL.Query()["game-type"]
		if !hasGameType {
			rows, err = db.Query("select block_number, game_type, player, amount from recent_games order by block_number desc limit 25;")
		} else {
			rows, err = db.Query("select block_number, game_type, player, amount from recent_games WHERE game_type = ? order by block_number desc limit 25;", gameType[0])
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var entry RecentGameEntry
			var amount float64
			err = rows.Scan(&entry.BlockNumber, &entry.GameType, &entry.Address, &amount)
			entry.WinAmount = amount / 1e9
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

	case "/metrics":
		fmt.Fprintf(w, "# TYPE last_block gauge\n")
		fmt.Fprintf(w, "last_block %d\n", atomic.LoadUint64(&last_block_metrics))

		fmt.Fprintf(w, "# TYPE total_reconnects counter\n")
		fmt.Fprintf(w, "total_reconnects %d\n", atomic.LoadUint32(&reconnect_attempts_metrics))
		return
	}
}