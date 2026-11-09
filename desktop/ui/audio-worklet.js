'use strict';
class SaberPCM extends AudioWorkletProcessor {
 constructor(){super();this.phase=0;this.sum=0;this.count=0;this.chunk=new Int16Array(320);this.offset=0;this.energy=0;}
 process(inputs){const samples=inputs[0]?.[0];if(!samples)return true;for(const value of samples){this.sum+=value;this.count++;this.phase+=16000;if(this.phase<sampleRate)continue;this.phase-=sampleRate;const sample=Math.max(-1,Math.min(1,this.sum/this.count));this.sum=0;this.count=0;this.chunk[this.offset++]=Math.round(sample<0?sample*32768:sample*32767);this.energy+=sample*sample;if(this.offset===320){const data=this.chunk.buffer;this.port.postMessage({pcm:data,rms:Math.sqrt(this.energy/320)},[data]);this.chunk=new Int16Array(320);this.offset=0;this.energy=0;}}return true;}
}
registerProcessor('saber-pcm',SaberPCM);
