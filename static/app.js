// Helper function to convert duration string to hours/minutes/seconds
function durationToHMS(durationStr) {
    const match = durationStr.match(/(\d+h)?(\d+m)?(\d+(?:\.\d+)?s)?/);
    let hours = 0, minutes = 0, seconds = 0;
    
    if (match) {
        if (match[1]) hours = parseInt(match[1]);
        if (match[2]) minutes = parseInt(match[2]);
        if (match[3]) seconds = parseInt(parseFloat(match[3]));
    }
    
    return { hours, minutes, seconds };
}

// Helper function to convert hours/minutes/seconds to duration string
function hmsToGoDuration(h, m, s) {
    h = parseInt(h) || 0;
    m = parseInt(m) || 0;
    s = parseInt(s) || 0;
    
    // Convert to total seconds, then format as Go duration string
    const totalSeconds = h * 3600 + m * 60 + s;
    if (totalSeconds === 0) return "0s";
    
    let result = "";
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const secs = totalSeconds % 60;
    
    if (hours > 0) result += `${hours}h`;
    if (minutes > 0) result += `${minutes}m`;
    if (secs > 0) result += `${secs}s`;
    
    return result || "0s";
}

// API base URL
const API_BASE = '/api';

// Toast helper
function showToast(message, isError = false) {
    const toast = document.getElementById('toast');
    const toastMessage = document.getElementById('toastMessage');
    toastMessage.textContent = message;
    toast.className = `toast align-items-center ${isError ? 'bg-danger text-white' : 'bg-success text-white'}`;
    const bsToast = new bootstrap.Toast(toast);
    bsToast.show();
}

// Format date
function formatDate(dateStr) {
    if (!dateStr || dateStr === '0001-01-01T00:00:00Z') return 'Never';
    const date = new Date(dateStr);
    return date.toLocaleString();
}

// Escape HTML for safe rendering of logs
function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// Load status
async function loadStatus() {
    try {
        const response = await fetch(`${API_BASE}/status`);
        const data = await response.json();
        if (data.success) {
            const versionText = data.data.yt_dlp_version ? data.data.yt_dlp_version : 'unknown';
            document.getElementById('status').innerHTML = `
                <span class="badge bg-success">Running</span>
                <span class="ms-3"><strong>Channels:</strong> ${data.data.channels_count}</span>
                <span class="ms-3"><strong>Videos:</strong> ${data.data.videos_count}</span>
                <span class="ms-3"><strong>yt-dlp:</strong> ${versionText}</span>
            `;
        }
    } catch (error) {
        document.getElementById('status').innerHTML = '<span class="badge bg-danger">Error</span>';
    }
}

// Load channels
async function loadChannels() {
    try {
        const response = await fetch(`${API_BASE}/channels`);
        const data = await response.json();
        if (data.success) {
            const channels = data.data || [];
            if (channels.length === 0) {
                document.getElementById('channelsList').innerHTML = '<p class="text-muted">No channels configured</p>';
            } else {
                document.getElementById('channelsList').innerHTML = channels.map((ch, idx) => {
                    const cutoffText = ch.cutoff_date && ch.cutoff_date !== '0001-01-01T00:00:00Z' 
                        ? `<span class="badge bg-info ms-2">From: ${new Date(ch.cutoff_date).toLocaleDateString()}</span>` 
                        : '';
                    const qualityText = ch.video_quality 
                        ? `<span class="badge bg-primary ms-2">Quality: ${ch.video_quality}</span>`
                        : '';
                    const hasError = ch.last_error && ch.last_error.trim().length > 0;
                    const errorBadge = hasError 
                        ? `<span class="badge bg-danger ms-2"><i class="bi bi-exclamation-circle"></i> Error</span>` 
                        : '';
                    const errorSection = hasError 
                        ? `
                            <div class="mt-2">
                                <button class="btn btn-sm btn-outline-danger collapsed" type="button" data-bs-toggle="collapse" data-bs-target="#error-${idx}" aria-expanded="false">
                                    <i class="bi bi-chevron-down"></i> View Error Details
                                </button>
                                <div class="collapse mt-2" id="error-${idx}">
                                    <div class="alert alert-danger" role="alert">
                                        <strong>Error:</strong> ${ch.last_error}<br>
                                        <small class="text-muted">At: ${formatDate(ch.last_error_time)}</small>
                                    </div>
                                </div>
                            </div>
                        ` 
                        : '';
                    return `
                        <div class="d-flex justify-content-between align-items-start border-bottom py-3">
                            <div style="flex-grow: 1;">
                                <strong>${ch.name}</strong>
                                <span class="badge bg-secondary ms-2">${ch.retention_days || 'default'} days</span>
                                ${cutoffText}
                                ${qualityText}
                                ${ch.download_shorts ? '<span class="badge bg-success ms-2">Shorts</span>' : ''}
                                ${errorBadge}<br>
                                <small class="text-muted">${ch.url}</small><br>
                                <span class="last-checked">Last checked: ${formatDate(ch.last_checked)}</span>
                                ${errorSection}
                            </div>
                            <div class="ms-3">
                                <button class="btn btn-warning btn-sm me-2" onclick="openEditChannelModal('${ch.id}', '${ch.name}', ${ch.retention_days}, '${ch.cutoff_date}', '${ch.video_quality}', ${ch.download_shorts})">
                                    <i class="bi bi-pencil"></i> Edit
                                </button>
                                <button class="btn btn-danger btn-sm" onclick="removeChannel('${ch.id}')">
                                    <i class="bi bi-trash"></i> Remove
                                </button>
                            </div>
                        </div>
                    `;
                }).join('');
            }
        }
    } catch (error) {
        document.getElementById('channelsList').innerHTML = '<p class="text-danger">Failed to load channels</p>';
    }
}

// Load videos
async function loadVideos() {
    try {
        const response = await fetch(`${API_BASE}/videos`);
        const data = await response.json();
        if (data.success) {
            const videos = data.data || [];
            if (videos.length === 0) {
                document.getElementById('videosList').innerHTML = '<p class="text-muted">No videos configured</p>';
            } else {
                document.getElementById('videosList').innerHTML = videos.map((vid, idx) => {
                    const hasError = vid.last_error && vid.last_error.trim().length > 0;
                    const errorBadge = hasError 
                        ? `<span class="badge bg-danger ms-2"><i class="bi bi-exclamation-circle"></i> Error</span>` 
                        : '';
                    const errorSection = hasError 
                        ? `
                            <div class="mt-2">
                                <button class="btn btn-sm btn-outline-danger collapsed" type="button" data-bs-toggle="collapse" data-bs-target="#video-error-${idx}" aria-expanded="false">
                                    <i class="bi bi-chevron-down"></i> View Error Details
                                </button>
                                <div class="collapse mt-2" id="video-error-${idx}">
                                    <div class="alert alert-danger" role="alert">
                                        <strong>Error:</strong> ${vid.last_error}<br>
                                        <small class="text-muted">At: ${formatDate(vid.last_error_time)}</small>
                                    </div>
                                </div>
                            </div>
                        ` 
                        : '';
                    return `
                        <div class="d-flex justify-content-between align-items-start border-bottom py-3">
                            <div style="flex-grow: 1;">
                                <strong>${vid.title}</strong>
                                <span class="badge bg-secondary ms-2">${vid.retention_days || 'default'} days</span>
                                ${vid.video_quality ? `<span class="badge bg-info ms-2">${vid.video_quality}</span>` : ''}
                                ${vid.download_shorts ? '<span class="badge bg-success ms-2">Shorts</span>' : ''}
                                ${errorBadge}<br>
                                <small class="text-muted">${vid.url}</small><br>
                                <span class="last-checked">Last checked: ${formatDate(vid.last_checked)}</span>
                                ${errorSection}
                            </div>
                            <button class="btn btn-danger btn-sm" onclick="removeVideo('${vid.id}')">
                                <i class="bi bi-trash"></i> Remove
                            </button>
                        </div>
                    `;
                }).join('');
            }
        }
    } catch (error) {
        document.getElementById('videosList').innerHTML = '<p class="text-danger">Failed to load videos</p>';
    }
}

// Load config
async function loadConfig() {
    try {
        const response = await fetch(`${API_BASE}/config`);
        const data = await response.json();
        if (data.success) {
            const config = data.data;
            
            // Load check interval
            const checkInterval = durationToHMS(config.check_interval_seconds);
            document.getElementById('checkIntervalH').value = checkInterval.hours;
            document.getElementById('checkIntervalM').value = checkInterval.minutes;
            document.getElementById('checkIntervalS').value = checkInterval.seconds;
            
            document.getElementById('retentionDays').value = config.retention_days;
            document.getElementById('downloadDir').value = config.download_dir;
            document.getElementById('fileNamePattern').value = config.file_name_pattern;
            document.getElementById('maxConcurrent').value = config.max_concurrent_downloads;
            
            const ytDlp = config.yt_dlp || {};
            
            // Load update interval
            const updateInterval = durationToHMS(ytDlp.update_interval_seconds || "24h0m0s");
            document.getElementById('ytDlpUpdateIntervalH').value = updateInterval.hours;
            document.getElementById('ytDlpUpdateIntervalM').value = updateInterval.minutes;
            document.getElementById('ytDlpUpdateIntervalS').value = updateInterval.seconds;
            
            // Load extractor sleep interval
            const sleepInterval = durationToHMS(ytDlp.extractor_sleep_interval_seconds || "0s");
            document.getElementById('extractorSleepIntervalH').value = sleepInterval.hours;
            document.getElementById('extractorSleepIntervalM').value = sleepInterval.minutes;
            document.getElementById('extractorSleepIntervalS').value = sleepInterval.seconds;
            
            document.getElementById('downloadThroughputLimit').value = ytDlp.download_throughput_limit || '';
            document.getElementById('ytDlpCacheDir').value = ytDlp.cache_dir || '';
            document.getElementById('restrictFilenames').checked = !!ytDlp.restrict_filenames;
            document.getElementById('cookiesBrowser').value = ytDlp.cookies_browser || '';
        }
    } catch (error) {
        showToast('Failed to load configuration', true);
    }
}

// Load recent logs
async function loadLogs() {
    try {
        const response = await fetch(`${API_BASE}/logs`);
        const data = await response.json();
        if (!data.success) {
            document.getElementById('logsList').innerHTML = '<span class="text-danger">Failed to load logs</span>';
            return;
        }

        const entries = (data.data && data.data.entries) ? data.data.entries : [];
        if (entries.length === 0) {
            document.getElementById('logsList').innerHTML = '<span class="text-muted">No logs available yet</span>';
            return;
        }

        document.getElementById('logsList').innerHTML = entries.map((line) => escapeHtml(line)).join('\n');

        const logsContainer = document.getElementById('logsList');
        logsContainer.scrollTop = logsContainer.scrollHeight;
    } catch (error) {
        document.getElementById('logsList').innerHTML = '<span class="text-danger">Failed to load logs</span>';
    }
}

// Save pasted cookies with confirmation
document.getElementById('saveCookiesBtn').addEventListener('click', async () => {
    const cookieText = document.getElementById('cookiesPaste').value.trim();
    if (!cookieText) {
        showToast('Please paste some cookies first', true);
        return;
    }

    // Check if cookies already exist
    let confirmMessage = 'Save these cookies?';
    try {
        const response = await fetch(`${API_BASE}/config`);
        const data = await response.json();
        if (data.data && data.data.yt_dlp && data.data.yt_dlp.cookies_file) {
            confirmMessage = 'This will overwrite your existing cookies. Continue?';
        }
    } catch (e) {
        // Continue regardless
    }

    if (!confirm(confirmMessage)) {
        return;
    }

    try {
        const response = await fetch(`${API_BASE}/cookies`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ cookie_text: cookieText })
        });
        const data = await response.json();
        if (data.success) {
            showToast('Cookies saved successfully!');
            document.getElementById('cookiesPaste').value = '';
            loadConfig();
        } else {
            showToast(data.message || 'Failed to save cookies', true);
        }
    } catch (error) {
        showToast('Failed to save cookies', true);
    }
});

// Clear all cookies
document.getElementById('clearCookiesBtn').addEventListener('click', async () => {
    if (!confirm('Are you sure you want to clear all cookies? This cannot be undone.')) {
        return;
    }

    try {
        const response = await fetch(`${API_BASE}/cookies/clear`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' }
        });
        const data = await response.json();
        if (data.success) {
            showToast('All cookies cleared!');
            document.getElementById('cookiesPaste').value = '';
            loadConfig();
        } else {
            showToast(data.message || 'Failed to clear cookies', true);
        }
    } catch (error) {
        showToast('Failed to clear cookies', true);
    }
});

// Add channel
document.getElementById('addChannelForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const name = document.getElementById('channelName').value;
    const url = document.getElementById('channelURL').value;
    const retention = parseInt(document.getElementById('channelRetention').value) || 0;
    const cutoffDate = document.getElementById('channelCutoffDate').value;
    const quality = document.getElementById('channelQuality').value;
    const downloadShorts = document.getElementById('channelDownloadShorts').checked;

    const channelData = { name, url, retention_days: retention, video_quality: quality, download_shorts: downloadShorts };
    if (cutoffDate) {
        channelData.cutoff_date = new Date(cutoffDate).toISOString();
    }

    try {
        const response = await fetch(`${API_BASE}/channels`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(channelData)
        });
        const data = await response.json();
        if (data.success) {
            showToast('Channel added successfully');
            document.getElementById('addChannelForm').reset();
            loadChannels();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to add channel', true);
        }
    } catch (error) {
        showToast('Failed to add channel', true);
    }
});

// Remove channel
async function removeChannel(id) {
    if (!confirm('Are you sure you want to remove this channel?')) return;

    try {
        const response = await fetch(`${API_BASE}/channels/${id}`, { method: 'DELETE' });
        const data = await response.json();
        if (data.success) {
            showToast('Channel removed successfully');
            loadChannels();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to remove channel', true);
        }
    } catch (error) {
        showToast('Failed to remove channel', true);
    }
}

// Open edit channel modal
function openEditChannelModal(id, name, retentionDays, cutoffDate, videoQuality, downloadShorts) {
    document.getElementById('editChannelId').value = id;
    document.getElementById('editChannelName').value = name;
    document.getElementById('editChannelRetention').value = retentionDays || '';
    
    // Convert ISO date to YYYY-MM-DD format for date input
    if (cutoffDate && cutoffDate !== '0001-01-01T00:00:00Z') {
        const date = new Date(cutoffDate);
        document.getElementById('editChannelCutoffDate').value = date.toISOString().split('T')[0];
    } else {
        document.getElementById('editChannelCutoffDate').value = '';
    }
    
    document.getElementById('editChannelQuality').value = videoQuality || '';
    document.getElementById('editChannelDownloadShorts').checked = downloadShorts || false;
    
    const modal = new bootstrap.Modal(document.getElementById('editChannelModal'));
    modal.show();
}

// Save channel edits
document.getElementById('saveChannelEditsBtn').addEventListener('click', async () => {
    const id = document.getElementById('editChannelId').value;
    const retentionDays = parseInt(document.getElementById('editChannelRetention').value) || 0;
    const cutoffDate = document.getElementById('editChannelCutoffDate').value;
    const videoQuality = document.getElementById('editChannelQuality').value;
    const downloadShorts = document.getElementById('editChannelDownloadShorts').checked;

    const updateData = {
        retention_days: retentionDays,
        video_quality: videoQuality,
        download_shorts: downloadShorts
    };

    // Only include cutoff date if it's set
    if (cutoffDate) {
        updateData.cutoff_date = new Date(cutoffDate).toISOString();
    } else {
        updateData.cutoff_date = '0001-01-01T00:00:00Z';
    }

    try {
        const response = await fetch(`${API_BASE}/channels/${id}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(updateData)
        });
        const data = await response.json();
        if (data.success) {
            showToast('Channel updated successfully');
            bootstrap.Modal.getInstance(document.getElementById('editChannelModal')).hide();
            loadChannels();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to update channel', true);
        }
    } catch (error) {
        showToast('Failed to update channel', true);
    }
});

// Add video
document.getElementById('addVideoForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const title = document.getElementById('videoTitle').value;
    const url = document.getElementById('videoURL').value;
    const retention = parseInt(document.getElementById('videoRetention').value) || 0;
    const quality = document.getElementById('videoQuality').value;
    const downloadShorts = document.getElementById('videoDownloadShorts').checked;

    try {
        const response = await fetch(`${API_BASE}/videos`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ title, url, retention_days: retention, video_quality: quality, download_shorts: downloadShorts })
        });
        const data = await response.json();
        if (data.success) {
            showToast('Video added successfully');
            document.getElementById('addVideoForm').reset();
            loadVideos();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to add video', true);
        }
    } catch (error) {
        showToast('Failed to add video', true);
    }
});

// Remove video
async function removeVideo(id) {
    if (!confirm('Are you sure you want to remove this video?')) return;

    try {
        const response = await fetch(`${API_BASE}/videos/${id}`, { method: 'DELETE' });
        const data = await response.json();
        if (data.success) {
            showToast('Video removed successfully');
            loadVideos();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to remove video', true);
        }
    } catch (error) {
        showToast('Failed to remove video', true);
    }
}

// Update config
document.getElementById('configForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    
    // Convert HMS to Go duration strings
    const checkIntervalStr = hmsToGoDuration(
        document.getElementById('checkIntervalH').value,
        document.getElementById('checkIntervalM').value,
        document.getElementById('checkIntervalS').value
    );
    const updateIntervalStr = hmsToGoDuration(
        document.getElementById('ytDlpUpdateIntervalH').value,
        document.getElementById('ytDlpUpdateIntervalM').value,
        document.getElementById('ytDlpUpdateIntervalS').value
    );
    const sleepIntervalStr = hmsToGoDuration(
        document.getElementById('extractorSleepIntervalH').value,
        document.getElementById('extractorSleepIntervalM').value,
        document.getElementById('extractorSleepIntervalS').value
    );
    
    const config = {
        check_interval_seconds: checkIntervalStr,
        retention_days: parseInt(document.getElementById('retentionDays').value),
        download_dir: document.getElementById('downloadDir').value,
        file_name_pattern: document.getElementById('fileNamePattern').value,
        max_concurrent_downloads: parseInt(document.getElementById('maxConcurrent').value),
        yt_dlp: {
            update_interval_seconds: updateIntervalStr,
            extractor_sleep_interval_seconds: sleepIntervalStr,
            download_throughput_limit: document.getElementById('downloadThroughputLimit').value.trim(),
            restrict_filenames: document.getElementById('restrictFilenames').checked,
            cache_dir: document.getElementById('ytDlpCacheDir').value.trim(),
            cookies_browser: document.getElementById('cookiesBrowser').value,
            cookies_file: "" // Set by cookie paste endpoint
        }
    };

    try {
        const response = await fetch(`${API_BASE}/config`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(config)
        });
        const data = await response.json();
        if (data.success) {
            showToast('Configuration saved successfully');
        } else {
            showToast(data.message || 'Failed to save configuration', true);
        }
    } catch (error) {
        showToast('Failed to save configuration', true);
    }
});

document.getElementById('refreshLogsBtn').addEventListener('click', () => {
    loadLogs();
});

// Initial load
loadStatus();
loadChannels();
loadVideos();
loadConfig();
loadLogs();

// Refresh data every 30 seconds
setInterval(() => {
    loadStatus();
    loadChannels();
    loadVideos();
    loadLogs();
}, 30000);
