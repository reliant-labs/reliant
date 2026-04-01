/**
 * Chunked downloader to work around Cloudflare connection closing issues
 * Downloads files in chunks using range requests, which avoids single long connections
 */

const https = require('https');
const http = require('http');
const fs = require('fs');
const path = require('path');
const log = require('./logger');

const CHUNK_SIZE = 10 * 1024 * 1024; // 10MB chunks
const MAX_CHUNK_RETRIES = 3;
const CHUNK_RETRY_DELAY = 1000; // 1 second

class ChunkedDownloader {
  constructor(url, outputPath, options = {}) {
    this.url = url;
    this.outputPath = outputPath;
    this.options = options;
    this.onProgress = options.onProgress || (() => {});
    this.urlObj = new URL(url);
    this.client = this.urlObj.protocol === 'https:' ? https : http;
  }

  async getFileSize() {
    return new Promise((resolve, reject) => {
      const req = this.client.request({
        hostname: this.urlObj.hostname,
        port: this.urlObj.port || (this.urlObj.protocol === 'https:' ? 443 : 80),
        path: this.urlObj.pathname,
        method: 'HEAD',
        headers: {
          'User-Agent': 'electron-updater',
          'Accept-Encoding': 'identity',
          ...this.options.headers
        }
      }, (res) => {
        const contentLength = parseInt(res.headers['content-length'] || '0', 10);
        if (contentLength > 0) {
          resolve(contentLength);
        } else {
          reject(new Error('Could not determine file size'));
        }
      });
      
      req.on('error', reject);
      req.setTimeout(30000, () => {
        req.destroy();
        reject(new Error('HEAD request timeout'));
      });
      req.end();
    });
  }

  async downloadChunk(start, end, totalSize, chunkNum, totalChunks) {
    const range = `bytes=${start}-${end}`;
    let lastError = null;
    
    for (let attempt = 1; attempt <= MAX_CHUNK_RETRIES; attempt++) {
      try {
        return await new Promise((resolve, reject) => {
          const req = this.client.request({
            hostname: this.urlObj.hostname,
            port: this.urlObj.port || (this.urlObj.protocol === 'https:' ? 443 : 80),
            path: this.urlObj.pathname,
            method: 'GET',
            headers: {
              'User-Agent': 'electron-updater',
              'Accept-Encoding': 'identity',
              'Range': range,
              ...this.options.headers
            },
            timeout: 60000 // 60 second timeout per chunk
          }, (res) => {
            if (res.statusCode !== 206 && res.statusCode !== 200) {
              reject(new Error(`Unexpected status: ${res.statusCode}`));
              return;
            }
            
            const chunks = [];
            res.on('data', (chunk) => chunks.push(chunk));
            res.on('end', () => {
              const data = Buffer.concat(chunks);
              resolve(data);
            });
            res.on('error', reject);
          });
          
          req.on('error', reject);
          req.on('timeout', () => {
            req.destroy();
            reject(new Error('Chunk download timeout'));
          });
          req.end();
        });
      } catch (error) {
        lastError = error;
        log.warn(`[ChunkedDownloader] Chunk ${chunkNum} attempt ${attempt} failed: ${error.message}`);
        
        if (attempt < MAX_CHUNK_RETRIES) {
          await new Promise(resolve => setTimeout(resolve, CHUNK_RETRY_DELAY * attempt));
        }
      }
    }
    
    throw new Error(`Failed to download chunk ${chunkNum} after ${MAX_CHUNK_RETRIES} attempts: ${lastError?.message}`);
  }

  async download() {
    log.info(`[ChunkedDownloader] Starting chunked download: ${this.url}`);
    
    // Get file size
    const totalSize = await this.getFileSize();
    log.info(`[ChunkedDownloader] File size: ${(totalSize / 1024 / 1024).toFixed(2)} MB`);
    
    // Clean up previous file
    if (fs.existsSync(this.outputPath)) {
      fs.unlinkSync(this.outputPath);
    }
    
    const fileHandle = await fs.promises.open(this.outputPath, 'w');
    const numChunks = Math.ceil(totalSize / CHUNK_SIZE);
    let downloadedBytes = 0;
    const startTime = Date.now();
    
    log.info(`[ChunkedDownloader] Downloading in ${numChunks} chunks`);
    
    try {
      for (let i = 0; i < numChunks; i++) {
        const start = i * CHUNK_SIZE;
        const end = Math.min(start + CHUNK_SIZE - 1, totalSize - 1);
        const chunkNum = i + 1;
        
        log.info(`[ChunkedDownloader] Downloading chunk ${chunkNum}/${numChunks} (${((end - start + 1) / 1024 / 1024).toFixed(2)} MB)`);
        
        const chunkStartTime = Date.now();
        const chunkData = await this.downloadChunk(start, end, totalSize, chunkNum, numChunks);
        const chunkTime = (Date.now() - chunkStartTime) / 1000;
        
        await fileHandle.write(chunkData, 0, chunkData.length, start);
        
        downloadedBytes += chunkData.length;
        const percent = (downloadedBytes / totalSize * 100);
        const elapsed = (Date.now() - startTime) / 1000;
        const avgSpeed = downloadedBytes / elapsed;
        
        // Report progress
        this.onProgress({
          percent: percent,
          transferred: downloadedBytes,
          total: totalSize,
          bytesPerSecond: avgSpeed
        });
        
        log.info(`[ChunkedDownloader] Chunk ${chunkNum} complete: ${percent.toFixed(1)}% @ ${(avgSpeed / 1024 / 1024).toFixed(2)} MB/s`);
        
        // Small delay between chunks to avoid overwhelming the server
        if (i < numChunks - 1) {
          await new Promise(resolve => setTimeout(resolve, 50));
        }
      }
      
      await fileHandle.close();
      
      // Verify file size
      const stats = fs.statSync(this.outputPath);
      if (stats.size !== totalSize) {
        throw new Error(`File size mismatch: expected ${totalSize}, got ${stats.size}`);
      }
      
      const totalTime = (Date.now() - startTime) / 1000;
      log.info(`[ChunkedDownloader] Download complete in ${totalTime.toFixed(2)}s`);
      
      return {
        path: this.outputPath,
        size: totalSize
      };
      
    } catch (error) {
      await fileHandle.close();
      if (fs.existsSync(this.outputPath)) {
        fs.unlinkSync(this.outputPath);
      }
      throw error;
    }
  }
}

module.exports = ChunkedDownloader;
