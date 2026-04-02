package fortnox

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"stripe-fortnox-sync/internal/db"
)

// InvoiceService creates Fortnox invoices and records invoice payments for Stripe charges.
type InvoiceService struct {
	api      Poster
	queries  *db.Queries
	resolver *MappingResolver
}

func NewInvoiceService(api *APIClient, queries *db.Queries, resolver *MappingResolver) *InvoiceService {
	return &InvoiceService{api: api, queries: queries, resolver: resolver}
}

type fortnoxCustomerRequest struct {
	Customer struct {
		Name        string `json:"Name"`
		Email       string `json:"Email,omitempty"`
		CountryCode string `json:"CountryCode,omitempty"`
	} `json:"Customer"`
}

type fortnoxCustomerResponse struct {
	Customer struct {
		CustomerNumber string `json:"CustomerNumber"`
	} `json:"Customer"`
}

type fortnoxInvoiceRow struct {
	AccountNumber     int     `json:"AccountNumber"`
	Description       string  `json:"Description"`
	Price             float64 `json:"Price"`
	VAT               int     `json:"VAT"`
	DeliveredQuantity float64 `json:"DeliveredQuantity"`
}

type fortnoxInvoiceRequest struct {
	Invoice struct {
		CustomerNumber            string              `json:"CustomerNumber"`
		Currency                  string              `json:"Currency"`
		InvoiceDate               string              `json:"InvoiceDate"`
		VATIncluded               bool                `json:"VATIncluded"`
		Comments                  string              `json:"Comments,omitempty"`
		ExternalInvoiceReference1 string              `json:"ExternalInvoiceReference1,omitempty"`
		InvoiceRows               []fortnoxInvoiceRow `json:"InvoiceRows"`
	} `json:"Invoice"`
}

type fortnoxInvoiceResponse struct {
	Invoice struct {
		DocumentNumber string `json:"DocumentNumber"`
	} `json:"Invoice"`
}

type fortnoxInvoicePaymentRequest struct {
	InvoicePayment struct {
		InvoiceNumber        int     `json:"InvoiceNumber"`
		AmountCurrency       float64 `json:"AmountCurrency"`
		PaymentDate          string  `json:"PaymentDate"`
		ModeOfPaymentAccount int     `json:"ModeOfPaymentAccount"`
	} `json:"InvoicePayment"`
}

// EnsureFortnoxCustomer returns the Fortnox CustomerNumber for a Stripe customer,
// creating a new one in Fortnox if it doesn't exist yet.
func (s *InvoiceService) EnsureFortnoxCustomer(ctx context.Context, customer *db.StripeCustomer) (string, error) {
	if customer.FortnoxCustomerID.Valid && customer.FortnoxCustomerID.String != "" {
		return customer.FortnoxCustomerID.String, nil
	}

	req := fortnoxCustomerRequest{}
	name := customer.ID // fallback to Stripe customer ID
	if customer.Name.Valid && customer.Name.String != "" {
		name = customer.Name.String
	} else if customer.Email.Valid && customer.Email.String != "" {
		name = customer.Email.String
	}
	req.Customer.Name = name
	if customer.Email.Valid {
		req.Customer.Email = customer.Email.String
	}
	if customer.Country.Valid {
		req.Customer.CountryCode = customer.Country.String
	}

	respBody, err := s.api.Post(ctx, "customers", req)
	if err != nil {
		return "", fmt.Errorf("create fortnox customer: %w", err)
	}

	var resp fortnoxCustomerResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("parse customer response: %w", err)
	}
	if resp.Customer.CustomerNumber == "" {
		return "", fmt.Errorf("fortnox returned empty customer number")
	}

	if err := s.queries.SetFortnoxCustomerID(ctx, resp.Customer.CustomerNumber, customer.ID); err != nil {
		return "", fmt.Errorf("save fortnox customer id: %w", err)
	}

	return resp.Customer.CustomerNumber, nil
}

// CreateInvoice posts a Fortnox invoice for a Stripe charge and stores the returned invoice number.
// Idempotent: if the charge already has fortnox_invoice_number, returns it without calling Fortnox.
func (s *InvoiceService) CreateInvoice(ctx context.Context, charge db.StripeCharge, customer *db.StripeCustomer) (string, error) {
	if charge.FortnoxInvoiceNumber.Valid && charge.FortnoxInvoiceNumber.String != "" {
		return charge.FortnoxInvoiceNumber.String, nil
	}

	customerNum, err := s.EnsureFortnoxCustomer(ctx, customer)
	if err != nil {
		return "", err
	}

	countryCode := ""
	if charge.BillingCountry.Valid && charge.BillingCountry.String != "" {
		countryCode = charge.BillingCountry.String
	} else if customer != nil && customer.Country.Valid && customer.Country.String != "" {
		countryCode = customer.Country.String
	}

	mapping, err := s.resolver.RevenueMapping(ctx, countryCode)
	if err != nil {
		return "", fmt.Errorf("resolve revenue mapping: %w", err)
	}

	accountNum, err := strconv.Atoi(mapping.Konto)
	if err != nil {
		return "", fmt.Errorf("invalid account number %q: %w", mapping.Konto, err)
	}

	vatRate := 0
	if mapping.Momssats.Valid {
		vatRate = int(mapping.Momssats.Float64)
	}

	req := fortnoxInvoiceRequest{}
	req.Invoice.CustomerNumber = customerNum
	req.Invoice.Currency = strings.ToUpper(charge.Currency)
	req.Invoice.InvoiceDate = time.Unix(charge.CreatedAt, 0).Format("2006-01-02")
	req.Invoice.VATIncluded = true
	req.Invoice.Comments = "Stripe " + charge.ID
	req.Invoice.ExternalInvoiceReference1 = charge.ID
	req.Invoice.InvoiceRows = []fortnoxInvoiceRow{
		{
			AccountNumber:     accountNum,
			Description:       charge.ID,
			Price:             toMajorUnit(charge.Amount),
			VAT:               vatRate,
			DeliveredQuantity: 1,
		},
	}

	respBody, err := s.api.Post(ctx, "invoices", req)
	if err != nil {
		return "", fmt.Errorf("create fortnox invoice: %w", err)
	}

	var resp fortnoxInvoiceResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("parse invoice response: %w", err)
	}
	if resp.Invoice.DocumentNumber == "" {
		return "", fmt.Errorf("fortnox returned empty invoice number")
	}

	invoiceNum := resp.Invoice.DocumentNumber

	// Book the invoice immediately so Fortnox creates the B-series accounting voucher.
	if _, err := s.api.Put(ctx, "invoices/"+invoiceNum+"/bookkeep", nil); err != nil {
		return "", fmt.Errorf("bookkeep fortnox invoice: %w", err)
	}

	if err := s.queries.SetChargeFortnoxInvoiceNumber(ctx, charge.ID, invoiceNum); err != nil {
		return "", fmt.Errorf("save invoice number: %w", err)
	}

	return invoiceNum, nil
}

// MarkInvoicePaid records an invoice payment in Fortnox, crediting the Stripe clearing account (1521).
// amountOre is the full invoice amount in the invoice's currency (smallest unit).
// chargeID is stored locally to track that this payment has been recorded (idempotency).
func (s *InvoiceService) MarkInvoicePaid(ctx context.Context, invoiceNumber, chargeID string, amountOre int64, paymentDate time.Time) error {
	clearingKonto, err := s.resolver.AccountByKontotyp(ctx, KontotypAvstämningskonto, "SEK")
	if err != nil {
		return fmt.Errorf("resolve clearing account: %w", err)
	}
	clearingNum, err := strconv.Atoi(clearingKonto)
	if err != nil {
		return fmt.Errorf("invalid clearing account %q: %w", clearingKonto, err)
	}
	invoiceNum, err := strconv.Atoi(invoiceNumber)
	if err != nil {
		return fmt.Errorf("invalid invoice number %q: %w", invoiceNumber, err)
	}

	req := fortnoxInvoicePaymentRequest{}
	req.InvoicePayment.InvoiceNumber = invoiceNum
	req.InvoicePayment.AmountCurrency = toMajorUnit(amountOre)
	req.InvoicePayment.PaymentDate = paymentDate.Format("2006-01-02")
	req.InvoicePayment.ModeOfPaymentAccount = clearingNum

	if _, err := s.api.Post(ctx, "invoicepayments", req); err != nil {
		return fmt.Errorf("post invoicepayment: %w", err)
	}

	if chargeID != "" {
		if err := s.queries.SetChargeInvoicePaid(ctx, chargeID); err != nil {
			log.Printf("set invoice paid flag for charge %s: %v", chargeID, err)
		}
	}

	return nil
}
