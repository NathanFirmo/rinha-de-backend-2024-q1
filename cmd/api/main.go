package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type CreateTransactionDto struct {
	Value       int    `json:"valor"`
	Type        string `json:"tipo"`
	Description string `json:"descricao"`
}

type BalanceResponseDto struct {
	Amount        int       `json:"total"`
	Limit         int       `json:"limite"`
	StatementDate time.Time `json:"data_extrato"`
}

type TransactionResponseDto struct {
	Amount      int       `json:"valor"`
	Type        string    `json:"tipo"`
	Description string    `json:"descricao"`
	CreatedAt   time.Time `json:"realizada_em"`
}

type StatementResponseDto struct {
	Balance            BalanceResponseDto       `json:"saldo"`
	LatestTransactions []TransactionResponseDto `json:"ultimas_transacoes"`
}

func main() {
	godotenv.Load(".env")

	app := fiber.New(fiber.Config{
		JSONEncoder: sonic.Marshal,
		JSONDecoder: sonic.Unmarshal,
	})

	dbConfig, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create a config: %v\n", err)
		os.Exit(1)
	}

	dbConfig.MaxConns = 25
	dbConfig.MinConns = 2
	dbConfig.MaxConnLifetime = time.Hour
	dbConfig.MaxConnIdleTime = time.Minute * 30
	dbConfig.HealthCheckPeriod = time.Minute
	dbConfig.ConnConfig.ConnectTimeout = time.Second * 5

	pool, err := pgxpool.NewWithConfig(context.Background(), dbConfig)
	if err != nil {
		fmt.Println(fmt.Errorf("Unable to create connection pool %v", err))
		os.Exit(1)
	}
	defer pool.Close()

	err = pool.Ping(context.Background())
	if err != nil {
		fmt.Println(fmt.Errorf("Unable to ping database: %v", err))
		os.Exit(1)
	}

	app.Post("/clientes/:id/transacoes", func(c *fiber.Ctx) error {
		return handleTransactionCreation(c, pool)
	})

	app.Get("/clientes/:id/extrato", func(c *fiber.Ctx) error {
		return handleStatement(c, pool)
	})

	app.Listen(":9999")
}

func handleTransactionCreation(c *fiber.Ctx, pool *pgxpool.Pool) error {
	id, err := strconv.Atoi(c.Params("id"))

	if err != nil {
		fmt.Println(fmt.Errorf("Invalid param id (%s) %v", c.Params("id"), err))
		return c.SendStatus(422)
	}

	if id < 1 || id > 5 {
		fmt.Println(fmt.Errorf("Id %d not found", id))
		return c.SendStatus(404)
	}

	var dto CreateTransactionDto

	err = c.BodyParser(&dto)

	if err != nil {
		fmt.Println(fmt.Errorf("Unable to parse body %v", err))
		return c.SendStatus(422)
	}

	if len(dto.Description) < 1 || len(dto.Description) > 10 {
		fmt.Println("Descricao must have between 1 and 10 characters")
		return c.SendStatus(422)
	}

	if dto.Type != "c" && dto.Type != "d" {
		fmt.Println(fmt.Errorf("Invalid type: %s", dto.Type))
		return c.SendStatus(422)
	}

	var balance, limit int
	err = pool.QueryRow(c.Context(), "SELECT balance, \"limit\" FROM bank.clients c WHERE c.id = $1;", id).Scan(&balance, &limit)

	if err != nil {
		fmt.Println(err)
		return c.SendStatus(500)
	}

	if dto.Type == "d" {
		balance -= dto.Value
		if balance < -limit {
			return c.SendStatus(422)
		}
	} else {
		balance += dto.Value
	}

	_, err = pool.Exec(c.Context(),
		"UPDATE bank.clients	SET \"balance\"=$1	WHERE id=$2;",
		balance,
		id,
	)

	_, err = pool.Exec(c.Context(),
		"INSERT INTO bank.transactions (client_id,amount,description,type,created_at)	VALUES ($1,$2,$3,$4,$5)",
		id,
		dto.Value,
		dto.Description,
		dto.Type,
		time.Now())

	if err != nil {
		fmt.Println(fmt.Errorf("Unable to save transaction %v", err))
		return c.SendStatus(500)
	}

	return c.Status(200).JSON(fiber.Map{
		"limite": limit,
		"saldo":  balance,
	})
}

func handleStatement(c *fiber.Ctx, pool *pgxpool.Pool) error {
	id, err := strconv.Atoi(c.Params("id"))

	if err != nil {
		fmt.Println(fmt.Errorf("Invalid param id (%s) %v", c.Params("id"), err))
		return c.SendStatus(422)
	}

	if id < 1 || id > 5 {
		fmt.Println(fmt.Errorf("Id %d not found", id))
		return c.SendStatus(404)
	}

	rows, err := pool.Query(c.Context(),
		`
		    SELECT
		      "limit",
		      balance,
		      amount,
		      description,
		      "type",
		      created_at
		    FROM
		      bank.clients c
		    LEFT JOIN bank.transactions t ON
		      t.client_id = c.id
		    WHERE
		      c.id = $1
        ORDER BY
          t.id DESC
        LIMIT 10
		  `,
		id,
	)
	defer rows.Close()

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.SendStatus(404)
		}

		fmt.Println(err)
		return c.SendStatus(500)
	}

	res := StatementResponseDto{
		LatestTransactions: make([]TransactionResponseDto, 0, 10),
	}

	for rows.Next() {
		var bl BalanceResponseDto
		var tr TransactionResponseDto

		err = rows.Scan(&bl.Limit, &bl.Amount, &tr.Amount, &tr.Description, &tr.Type, &tr.CreatedAt)
		if err != nil {
			if bl.Limit != 0 {
				res.Balance.Amount = bl.Amount
				res.Balance.Limit = bl.Limit
				res.Balance.StatementDate = time.Now()
				return c.Status(200).JSON(res)
			}

			fmt.Println(fmt.Errorf("Unable to scan row %v", err))
			return c.SendStatus(500)
		}

		res.Balance.Amount = bl.Amount
		res.Balance.Limit = bl.Limit
		res.Balance.StatementDate = time.Now()
		res.LatestTransactions = append(res.LatestTransactions, tr)
	}

	return c.Status(200).JSON(res)
}
