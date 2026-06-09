#include "qrcode_display.h"
#include "qrcodegen.h"

#include <lvgl.h>
#include <esp_log.h>
#include <esp_timer.h>
#include <cstring>
#include <cstdlib>
#include <vector>

static const char* TAG = "QRCode";

LV_FONT_DECLARE(BUILTIN_TEXT_FONT);

// Track the overlay objects so we can clean up
static lv_obj_t* qr_overlay_ = nullptr;
static lv_obj_t* qr_title_ = nullptr;
static std::vector<lv_obj_t*> qr_modules_;
static esp_timer_handle_t qr_timeout_timer_ = nullptr;

static void qr_cleanup() {
    if (qr_timeout_timer_) {
        esp_timer_stop(qr_timeout_timer_);
        esp_timer_delete(qr_timeout_timer_);
        qr_timeout_timer_ = nullptr;
    }
    for (auto* obj : qr_modules_) {
        if (obj) lv_obj_delete(obj);
    }
    qr_modules_.clear();
    if (qr_title_) { lv_obj_delete(qr_title_); qr_title_ = nullptr; }
    if (qr_overlay_) { lv_obj_delete(qr_overlay_); qr_overlay_ = nullptr; }
    ESP_LOGI(TAG, "QR code display cleaned up");
}

static void qr_timeout_cb(void* arg) {
    ESP_LOGI(TAG, "QR code display timeout");
    qr_cleanup();
}

bool ShowQrCode(const std::string& url, int timeout_seconds) {
    // Clean up any existing QR display first
    if (qr_overlay_) qr_cleanup();

    ESP_LOGI(TAG, "Generating QR code for: %s", url.c_str());

    uint8_t qrcode[qrcodegen_BUFFER_LEN_FOR_VERSION(7)];
    uint8_t temp[qrcodegen_BUFFER_LEN_FOR_VERSION(7)];

    if (!qrcodegen_encodeText(url.c_str(), temp, qrcode, qrcodegen_Ecc_LOW,
                              1, 7, qrcodegen_Mask_AUTO, true)) {
        if (!qrcodegen_encodeText(url.c_str(), temp, qrcode, qrcodegen_Ecc_LOW,
                                  1, 10, qrcodegen_Mask_AUTO, true)) {
            ESP_LOGE(TAG, "Failed to generate QR code (even at version 10)");
            return false;
        }
    }

    int size = qrcodegen_getSize(qrcode);
    ESP_LOGI(TAG, "QR code generated: %dx%d modules", size, size);

    // Get the active screen
    lv_obj_t* screen = lv_scr_act();
    int screen_w = lv_obj_get_width(screen);
    int screen_h = lv_obj_get_height(screen);

    // Create overlay
    qr_overlay_ = lv_obj_create(screen);
    lv_obj_set_size(qr_overlay_, screen_w, screen_h);
    lv_obj_set_pos(qr_overlay_, 0, 0);
    lv_obj_set_style_bg_color(qr_overlay_, lv_color_white(), 0);
    lv_obj_set_style_bg_opa(qr_overlay_, LV_OPA_COVER, 0);
    lv_obj_set_style_border_width(qr_overlay_, 0, 0);
    lv_obj_set_style_pad_all(qr_overlay_, 0, 0);
    lv_obj_set_style_radius(qr_overlay_, 0, 0);

    // Calculate QR code size - use most of the smaller screen dimension
    int qr_display_size = (screen_w < screen_h ? screen_w : screen_h) * 3 / 4;
    int module_size = qr_display_size / (size + 8); // 4-module quiet zone on each side
    if (module_size < 2) module_size = 2;
    int qr_pixel_size = module_size * size;
    int offset_x = (screen_w - qr_pixel_size) / 2;
    int offset_y = 50; // leave room for title on top

    // Title label
    qr_title_ = lv_label_create(qr_overlay_);
    lv_label_set_text(qr_title_, "喵小智绑定二维码\n请用微信扫码");
    lv_obj_set_style_text_color(qr_title_, lv_color_black(), 0);
    lv_obj_set_style_text_font(qr_title_, &BUILTIN_TEXT_FONT, 0);
    lv_obj_set_style_text_align(qr_title_, LV_TEXT_ALIGN_CENTER, 0);
    lv_obj_align(qr_title_, LV_ALIGN_TOP_MID, 0, 10);

    // Draw QR modules
    qr_modules_.reserve(size * size);
    for (int y = 0; y < size; y++) {
        for (int x = 0; x < size; x++) {
            if (qrcodegen_getModule(qrcode, x, y)) {
                lv_obj_t* mod = lv_obj_create(qr_overlay_);
                lv_obj_set_size(mod, module_size, module_size);
                lv_obj_set_pos(mod, offset_x + x * module_size, offset_y + y * module_size);
                lv_obj_set_style_bg_color(mod, lv_color_black(), 0);
                lv_obj_set_style_bg_opa(mod, LV_OPA_COVER, 0);
                lv_obj_set_style_border_width(mod, 0, 0);
                lv_obj_set_style_pad_all(mod, 0, 0);
                lv_obj_set_style_radius(mod, 0, 0);
                qr_modules_.push_back(mod);
            }
        }
    }

    // Set timeout to auto-dismiss the QR code
    esp_timer_create_args_t timer_args = {
        .callback = qr_timeout_cb,
        .arg = nullptr,
        .dispatch_method = ESP_TIMER_TASK,
        .name = "qr_timeout",
    };
    esp_timer_create(&timer_args, &qr_timeout_timer_);
    esp_timer_start_once(qr_timeout_timer_, timeout_seconds * 1000000);

    ESP_LOGI(TAG, "QR code displayed: %lu modules, %ds timeout", qr_modules_.size(), timeout_seconds);
    return true;
}

void HideQrCode() {
    qr_cleanup();
}
