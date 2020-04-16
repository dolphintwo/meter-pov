FROM dfinlab/meter-pos:latest AS pos
FROM dfinlab/meter-pow:latest AS pow

FROM ubuntu:18.04

# POS settings 
COPY --from=pos /usr/bin/meter /usr/bin/
COPY --from=pos /usr/bin/disco /usr/bin/
COPY --from=pos /usr/lib/libpbc.so* /usr/lib/
ENV LD_LIBRARY_PATH=/usr/lib

# POW settings
COPY --from=pow /usr/local/bin/bitcoind /usr/bin/
COPY --from=pow /usr/local/bin/bitcoin-cli /usr/bin/
COPY --from=pow /usr/local/bin/bitcoin-tx /usr/bin/

COPY --from=pow /usr/lib/libboost*.so* /usr/lib/
COPY --from=pow /usr/lib/libssl*.so* /usr/lib/
COPY --from=pow /usr/lib/libevent*.so* /usr/lib/
COPY --from=pow /usr/lib/libcrypto*.so* /usr/lib/
COPY --from=pow /usr/lib/libminiupnpc*.so* /usr/lib/
COPY --from=pow /usr/lib/libzmq*.so* /usr/lib/
COPY --from=pow /usr/lib/libstdc++*.so* /usr/lib/
COPY --from=pow /usr/lib/libsodium*.so* /usr/lib/
COPY --from=pow /usr/lib/libpgm*.so* /usr/lib/
COPY --from=pow /usr/lib/libnorm*.so* /usr/lib/
COPY --from=pow /usr/lib/libdb*.so* /usr/lib/

# necessary packages
RUN apt-get update && apt-get install -y --no-install-recommends supervisor rsyslog rsyslog-relp vim-tiny && apt clean
# RUN apt-get update && apt-get install -y python-pip python-setuptools python-wheel && apt clean
# RUN pip install supervisor-stdout

ENV POS_EXTRA=
ENV POW_EXTRA=

RUN mkdir /pow
RUN mkdir /pos

COPY allin/bitcoin.conf /pow/bitcoin.conf
COPY allin/00-meter.conf /etc/rsyslog.d/
COPY allin/rsyslog.conf /etc/rsyslog.conf
COPY allin/supervisord.conf /etc/supervisor/conf.d/supervisord.conf
COPY allin/reset.sh /
RUN chmod a+x /reset.sh

RUN touch /var/log/supervisor/pos-stdout.log
RUN touch /var/log/supervisor/pos-stderr.log

RUN touch /root/.bashrc && cat 'alias posout="tail -f /var/log/supervisor/pos-stdout.log"' >> /root/.bashrc
RUN touch /root/.bashrc && cat 'alias poserr="tail -f /var/log/supervisor/pos-stderr.log"' >> /root/.bashrc
RUN touch /root/.bashrc && cat 'alias powout="tail -f /var/log/supervisor/pow-stdout.log"' >> /root/.bashrc
RUN touch /root/.bashrc && cat 'alias powerr="tail -f /var/log/supervisor/pow-stderr.log"' >> /root/.bashrc

LABEL com.centurylinklabs.watchtower.lifecycle.pre-update="/reset.sh"

EXPOSE 8668 8669 8670 8671 11235 11235/udp 55555/udp 8332 9209
ENTRYPOINT [ "/usr/bin/supervisord" ]
