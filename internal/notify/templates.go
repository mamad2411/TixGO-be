package notify

// Katalog templat. Variabel memakai {{.key}}. Teks sengaja singkat dan jelas (docs/16 §7: santai, "kamu").
var catalog = map[string]map[string]tpl{
	"verify_email": {
		"id": {"Verifikasi email TixGo kamu", "Halo {{.name}},\n\nKlik tautan ini untuk memverifikasi email kamu:\n{{.link}}\n\nTautan berlaku {{.hours}} jam. Kalau bukan kamu yang mendaftar, abaikan email ini."},
		"en": {"Verify your TixGo email", "Hi {{.name}},\n\nOpen this link to verify your email:\n{{.link}}\n\nThe link is valid for {{.hours}} hours. If this wasn't you, ignore this email."},
	},
	"otp_code": {
		"id": {"Kode verifikasi TixGo", "Halo {{.name}},\n\nKode verifikasimu: {{.code}}\n\nBerlaku {{.minutes}} menit dan hanya bisa dipakai sekali. Jangan bagikan ke siapa pun, termasuk yang mengaku dari TixGo. Kalau bukan kamu yang meminta, abaikan email ini dan segera ganti kata sandimu."},
		"en": {"Your TixGo verification code", "Hi {{.name}},\n\nYour verification code: {{.code}}\n\nValid for {{.minutes}} minutes, single use. Never share it with anyone, including people claiming to be TixGo. If you did not request it, ignore this email and change your password."},
	},
	"reset_password": {
		"id": {"Atur ulang kata sandi TixGo", "Halo {{.name}},\n\nKlik tautan ini untuk mengatur ulang kata sandi:\n{{.link}}\n\nTautan berlaku {{.minutes}} menit dan hanya bisa dipakai sekali. Kalau bukan kamu, abaikan email ini."},
		"en": {"Reset your TixGo password", "Hi {{.name}},\n\nUse this link to reset your password:\n{{.link}}\n\nIt is valid for {{.minutes}} minutes and works once."},
	},
	"password_changed": {
		"id": {"Kata sandi TixGo kamu berubah", "Halo {{.name}},\n\nKata sandi akunmu baru saja diubah. Semua perangkat lain sudah dikeluarkan. Kalau bukan kamu, segera hubungi bantuan."},
		"en": {"Your TixGo password changed", "Hi {{.name}},\n\nYour password was just changed and other devices were signed out. If this wasn't you, contact support now."},
	},
	"email_change_confirm": {
		"id": {"Konfirmasi email baru TixGo", "Halo {{.name}},\n\nKlik untuk mengonfirmasi email baru kamu:\n{{.link}}"},
		"en": {"Confirm your new TixGo email", "Hi {{.name}},\n\nOpen this link to confirm your new email:\n{{.link}}"},
	},
	"email_change_notice": {
		"id": {"Email akun TixGo diganti", "Halo {{.name}},\n\nEmail akunmu diganti ke {{.new_email}}. Kalau bukan kamu, segera hubungi bantuan."},
		"en": {"TixGo account email changed", "Hi {{.name}},\n\nYour account email was changed to {{.new_email}}. If this wasn't you, contact support."},
	},
	"order_paid": {
		"id": {"Tiket {{.event_title}} sudah terbit", "Halo {{.name}},\n\nPembayaran untuk {{.event_title}} berhasil. Total {{.total}}.\nNomor pesanan: {{.order_id}}\nTiketmu ada di: {{.tickets_url}}\n\nKebijakan refund: {{.refund_summary}}"},
		"en": {"Your {{.event_title}} tickets are ready", "Hi {{.name}},\n\nPayment for {{.event_title}} succeeded. Total {{.total}}.\nOrder: {{.order_id}}\nYour tickets: {{.tickets_url}}\n\nRefund policy: {{.refund_summary}}"},
	},
	"order_expired": {
		"id": {"Pesanan {{.event_title}} kedaluwarsa", "Halo {{.name}},\n\nWaktu pembayaran habis dan kursi dilepas. Kamu bisa mencoba lagi selama tiket masih tersedia."},
		"en": {"Your {{.event_title}} order expired", "Hi {{.name}},\n\nThe payment window ended and the seats were released."},
	},
	"push": {
		"id": {"{{.title}}", "{{.body}}"},
		"en": {"{{.title}}", "{{.body}}"},
	},
	"ticket_claim": {
		"id": {"{{.from_name}} mengirim tiket {{.event_title}} untukmu", "Halo,\n\n{{.from_name}} mengirim tiket untuk {{.event_title}}. Klaim tiketmu di:\n{{.link}}\n\nTautan berlaku sampai {{.expires}} dan hanya bisa dipakai sekali."},
		"en": {"{{.from_name}} sent you a ticket for {{.event_title}}", "Hi,\n\n{{.from_name}} sent you a ticket for {{.event_title}}. Claim it here:\n{{.link}}"},
	},
	"ticket_received": {
		"id": {"Tiket {{.event_title}} masuk ke akunmu", "Halo {{.name}},\n\nTiket {{.event_title}} sudah ada di akunmu: {{.tickets_url}}"},
		"en": {"A {{.event_title}} ticket is in your account", "Hi {{.name}},\n\nA ticket for {{.event_title}} is now in your account: {{.tickets_url}}"},
	},
	"comp_ticket": {
		"id": {"Undangan {{.event_title}}", "Halo {{.name}},\n\nKamu mendapat tiket undangan untuk {{.event_title}}. Lihat tiketmu: {{.tickets_url}}"},
		"en": {"Invitation to {{.event_title}}", "Hi {{.name}},\n\nYou have a complimentary ticket for {{.event_title}}: {{.tickets_url}}"},
	},
	"refund_update": {
		"id": {"Status refund pesanan {{.order_id}}: {{.status}}", "Halo {{.name}},\n\nRefund {{.amount}} untuk pesanan {{.order_id}} kini berstatus: {{.status}}.\n{{.note}}"},
		"en": {"Refund for order {{.order_id}}: {{.status}}", "Hi {{.name}},\n\nYour refund of {{.amount}} for order {{.order_id}} is now: {{.status}}.\n{{.note}}"},
	},
	"refund_account_needed": {
		"id": {"Isi rekening untuk refund {{.event_title}}", "Halo {{.name}},\n\nRefund {{.amount}} perlu ditransfer manual. Isi rekening tujuan di:\n{{.link}}\n\nBatas pengisian: {{.deadline}}. Dana kamu aman dan tidak hilang."},
		"en": {"Add your bank account for the {{.event_title}} refund", "Hi {{.name}},\n\nYour refund of {{.amount}} must be transferred manually. Add your account here:\n{{.link}}\n\nDeadline: {{.deadline}}."},
	},
	"event_cancelled": {
		"id": {"{{.event_title}} dibatalkan", "Halo {{.name}},\n\nMohon maaf, {{.event_title}} dibatalkan oleh penyelenggara. Refund penuh atas jumlah yang kamu bayar akan diproses otomatis. {{.refund_info}}"},
		"en": {"{{.event_title}} was cancelled", "Hi {{.name}},\n\nSorry, {{.event_title}} was cancelled by the organizer. A full refund of what you paid will be processed. {{.refund_info}}"},
	},
	"event_rescheduled": {
		"id": {"Jadwal {{.event_title}} berubah", "Halo {{.name}},\n\n{{.event_title}} dijadwalkan ulang ke {{.new_date}}. Tiketmu tetap berlaku. Jika tidak bisa hadir, kamu bisa meminta refund sampai {{.window_until}}."},
		"en": {"{{.event_title}} was rescheduled", "Hi {{.name}},\n\n{{.event_title}} moved to {{.new_date}}. Your ticket stays valid. You can request a refund until {{.window_until}}."},
	},
	"event_postponed": {
		"id": {"{{.event_title}} ditunda", "Halo {{.name}},\n\n{{.event_title}} ditunda dan tanggal baru belum ditentukan. Tiketmu tetap berlaku. Kamu bisa meminta refund sampai {{.window_until}}."},
		"en": {"{{.event_title}} was postponed", "Hi {{.name}},\n\n{{.event_title}} was postponed; a new date is not set. Your ticket stays valid. Refund requests are open until {{.window_until}}."},
	},
	"broadcast": {
		"id": {"{{.subject}}", "{{.body}}\n\n— Penyelenggara {{.event_title}}"},
		"en": {"{{.subject}}", "{{.body}}\n\n— Organizer of {{.event_title}}"},
	},
	"support_reply": {
		"id": {"[#{{.number}}] Balasan bantuan TixGo", "Halo {{.name}},\n\nAda balasan untuk tiket bantuan #{{.number}} ({{.subject}}):\n\n{{.body}}\n\nLihat selengkapnya: {{.link}}"},
		"en": {"[#{{.number}}] TixGo support reply", "Hi {{.name}},\n\nThere is a reply to ticket #{{.number}} ({{.subject}}):\n\n{{.body}}\n\n{{.link}}"},
	},
	"support_received": {
		"id": {"[#{{.number}}] Permintaan bantuan kami terima", "Halo {{.name}},\n\nTiket bantuan #{{.number}} ({{.subject}}) sudah kami terima. Target respons: {{.sla}}."},
		"en": {"[#{{.number}}] We received your request", "Hi {{.name}},\n\nSupport ticket #{{.number}} ({{.subject}}) received. Target response: {{.sla}}."},
	},
	"dsr_update": {
		"id": {"Permintaan data pribadi: {{.status}}", "Halo {{.name}},\n\nPermintaan {{.kind}} kamu kini berstatus: {{.status}}.\n{{.note}}"},
		"en": {"Personal data request: {{.status}}", "Hi {{.name}},\n\nYour {{.kind}} request is now: {{.status}}.\n{{.note}}"},
	},
	"export_ready": {
		"id": {"Ekspor data kamu siap", "Halo {{.name}},\n\nBerkas ekspor sudah siap diunduh (berlaku sampai {{.expires}}):\n{{.link}}"},
		"en": {"Your export is ready", "Hi {{.name}},\n\nYour export is ready (valid until {{.expires}}):\n{{.link}}"},
	},
	"payout_update": {
		"id": {"Pencairan {{.amount}}: {{.status}}", "Halo {{.name}},\n\nPermintaan pencairan {{.amount}} kini berstatus: {{.status}}.\n{{.note}}"},
		"en": {"Payout {{.amount}}: {{.status}}", "Hi {{.name}},\n\nYour payout request of {{.amount}} is now: {{.status}}.\n{{.note}}"},
	},
	"kyb_update": {
		"id": {"Verifikasi organizer: {{.status}}", "Halo {{.name}},\n\nStatus verifikasi organizer {{.organizer}} kini: {{.status}}.\n{{.reason}}"},
		"en": {"Organizer verification: {{.status}}", "Hi {{.name}},\n\nVerification for {{.organizer}} is now: {{.status}}.\n{{.reason}}"},
	},
	"event_review_update": {
		"id": {"Event {{.event_title}}: {{.status}}", "Halo {{.name}},\n\nEvent {{.event_title}} kini berstatus: {{.status}}.\n{{.note}}"},
		"en": {"Event {{.event_title}}: {{.status}}", "Hi {{.name}},\n\nEvent {{.event_title}} is now: {{.status}}.\n{{.note}}"},
	},
	"waitlist_offer": {
		"id": {"Kursi {{.event_title}} tersedia lagi!", "Halo {{.name}},\n\nAda tiket {{.event_title}} yang tersedia untukmu. Kamu punya {{.minutes}} menit untuk membelinya:\n{{.link}}"},
		"en": {"{{.event_title}} tickets are available!", "Hi {{.name}},\n\nTickets for {{.event_title}} are available. You have {{.minutes}} minutes:\n{{.link}}"},
	},
	"reminder_onsale": {
		"id": {"{{.event_title}} mulai dijual {{.when}}", "Halo {{.name}},\n\nPenjualan tiket {{.event_title}} dimulai {{.when}}. Siapkan akun dan metode bayarmu: {{.link}}"},
		"en": {"{{.event_title}} goes on sale {{.when}}", "Hi {{.name}},\n\nTickets for {{.event_title}} go on sale {{.when}}: {{.link}}"},
	},
	"reminder_event": {
		"id": {"Pengingat: {{.event_title}} {{.when}}", "Halo {{.name}},\n\n{{.event_title}} berlangsung {{.when}} di {{.venue}}. Pintu dibuka {{.doors}}.\nTiketmu: {{.tickets_url}}"},
		"en": {"Reminder: {{.event_title}} {{.when}}", "Hi {{.name}},\n\n{{.event_title}} is {{.when}} at {{.venue}}. Doors {{.doors}}.\nYour tickets: {{.tickets_url}}"},
	},
	"follow_new_event": {
		"id": {"Konser baru: {{.event_title}}", "Halo {{.name}},\n\n{{.reason}} Lihat {{.event_title}}: {{.link}}"},
		"en": {"New event: {{.event_title}}", "Hi {{.name}},\n\n{{.reason}} Check out {{.event_title}}: {{.link}}"},
	},
	"squad_invite": {
		"id": {"{{.leader}} mengajakmu patungan tiket", "Halo,\n\n{{.leader}} mengajakmu bergabung di squad untuk {{.event_title}}. Kode squad: {{.code}}\n{{.link}}"},
		"en": {"{{.leader}} invited you to a ticket squad", "Hi,\n\n{{.leader}} invited you to a squad for {{.event_title}}. Code: {{.code}}\n{{.link}}"},
	},
	"resale_sold": {
		"id": {"Tiket {{.event_title}} kamu terjual", "Halo {{.name}},\n\nTiket {{.event_title}} yang kamu jual laku seharga {{.price}}. Isi rekening tujuan agar dana bisa kami transfer:\n{{.link}}"},
		"en": {"Your {{.event_title}} ticket sold", "Hi {{.name}},\n\nYour listed ticket for {{.event_title}} sold for {{.price}}. Add your bank account so we can transfer the funds:\n{{.link}}"},
	},
}

// SMS templat sederhana.
func OTPSMS(locale, code string, minutes int) string {
	if locale == "en" {
		return "TixGo code: " + code + ". Valid " + itoa(minutes) + " min. Never share it."
	}
	return "Kode TixGo: " + code + ". Berlaku " + itoa(minutes) + " menit. Jangan bagikan ke siapa pun."
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
