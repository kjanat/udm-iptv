/* Exercise the real receive dispatcher, report handlers and timer callbacks.
 * Only wall clock, packet I/O and kernel membership side effects are replaced.
 * This harness intentionally compiles against both original and patched source.
 */
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <assert.h>
#include <string.h>
#include <sys/socket.h>
#include "data.h"
#include "handler.h"
#include "input.h"
#include "kernel_api.h"
#include "membership.h"

extern mcast_proxy mproxy;
static struct timeval now;
static unsigned char packet[128];
static size_t packet_length;
static int packet_interface;
static unsigned int general_sent, specific_sent;

int __wrap_get_sysuptime(struct timeval *result)
{
    *result = now;
    return 0;
}

ssize_t __wrap_recvmsg(int socket, struct msghdr *message, int flags)
{
    struct cmsghdr *control;
    struct in_pktinfo info;
    (void)socket;
    (void)flags;
    assert(message->msg_iov[0].iov_len >= packet_length);
    memcpy(message->msg_iov[0].iov_base, packet, packet_length);
    memset(&info, 0, sizeof(info));
    info.ipi_ifindex = packet_interface;
    control = CMSG_FIRSTHDR(message);
    control->cmsg_level = IPPROTO_IP;
    control->cmsg_type = IP_PKTINFO;
    control->cmsg_len = CMSG_LEN(sizeof(info));
    memcpy(CMSG_DATA(control), &info, sizeof(info));
    message->msg_controllen = CMSG_SPACE(sizeof(info));
    memset(message->msg_name, 0, message->msg_namelen);
    return packet_length;
}

void __wrap_send_igmp_mld_query(imp_interface *interface, im_version version,
    pi_addr *destination, pi_addr *group, imp_source *sources, int suppress)
{
    (void)interface;
    (void)version;
    (void)destination;
    (void)sources;
    (void)suppress;
    if (group == NULL)
        general_sent++;
    else
        specific_sent++;
}

STATUS __wrap_k_mcast_join(pi_addr *address, char *name)
{
    (void)address;
    (void)name;
    return STATUS_OK;
}

STATUS __wrap_k_mcast_leave(pi_addr *address, char *name)
{
    (void)address;
    (void)name;
    return STATUS_OK;
}

void __wrap_imp_membership_db_update(pi_addr *address)
{
    (void)address;
}

static pi_addr address(const char *text)
{
    struct in_addr ipv4;
    pi_addr result;
    assert(inet_pton(AF_INET, text, &ipv4) == 1);
    imp_build_piaddr(AF_INET, &ipv4, &result);
    return result;
}

static imp_interface *interface_create(const char *name, int index)
{
    imp_interface *interface;
    imp_interface_create((char *)name, AF_INET, INTERFACE_DOWNSTREAM);
    interface = imp_interface_first();
    interface->if_index = index;
    interface->if_addr = address("192.0.2.20");
    interface->if_mtu = 1500;
    interface->startup = DEFAULT_RV;
    interface->gq_timer = imp_add_timer(general_queries_timer_handler, interface);
    imp_set_timer(2, interface->gq_timer);
    return interface;
}

static imp_interface *setup(void)
{
    memset(&mproxy, 0, sizeof(mproxy));
    mproxy.igmp_version = IM_IGMPv3_MLDv2;
    now.tv_sec = 100;
    now.tv_usec = 0;
    general_sent = specific_sent = 0;
    imp_init_timer();
    imp_interface_init();
    imp_membership_db_init();
    return interface_create("test0", 10);
}

static void finish(void)
{
    imp_interface_cleanup_all();
    imp_membership_db_cleanup_all();
    assert(imp_check_timer() == NULL);
}

static void query(imp_interface *interface, const char *source,
    int version, unsigned char response, unsigned char flags,
    unsigned char interval, const char *group, const char *queried_source)
{
    struct iphdr ip;
    struct in_addr ipv4;
    unsigned char *igmp = packet + 24;
    unsigned short checksum;
    size_t length = version == 2 ? 8 : 12;
    memset(packet, 0, sizeof(packet));
    memset(&ip, 0, sizeof(ip));
    ip.version = 4;
    ip.ihl = 6;
    ip.ttl = 1;
    ip.protocol = IPPROTO_IGMP;
    assert(inet_pton(AF_INET, source, &ipv4) == 1);
    ip.saddr = ipv4.s_addr;
    assert(inet_pton(AF_INET, group == NULL ? "224.0.0.1" : group, &ipv4) == 1);
    ip.daddr = ipv4.s_addr;
    memcpy(packet, &ip, sizeof(ip));
    packet[20] = 148;
    packet[21] = 4;
    igmp[0] = 0x11;
    igmp[1] = response;
    if (group != NULL) {
        assert(inet_pton(AF_INET, group, &ipv4) == 1);
        memcpy(igmp + 4, &ipv4, sizeof(ipv4));
    }
    if (version == 3) {
        igmp[8] = flags;
        igmp[9] = interval;
        if (queried_source != NULL) {
            igmp[11] = 1;
            assert(inet_pton(AF_INET, queried_source, &ipv4) == 1);
            memcpy(igmp + 12, &ipv4, sizeof(ipv4));
            length += 4;
        }
    }
    checksum = in_cusm((unsigned short *)igmp, length);
    memcpy(igmp + 2, &checksum, sizeof(checksum));
    packet_length = 24 + length;
    packet[2] = packet_length >> 8;
    packet[3] = packet_length & 255;
    checksum = in_cusm((unsigned short *)packet, 24);
    memcpy(packet + 10, &checksum, sizeof(checksum));
    packet_interface = interface->if_index;
    mcast_recv_igmp(0, mproxy.igmp_version);
}

static void lower(imp_interface *interface)
{
    query(interface, "192.0.2.10", 3, 100, 2, 125, NULL, NULL);
}

static imp_group *group_create(imp_interface *interface, im_version version)
{
    pi_addr group = address("239.192.0.1");
    return imp_group_create(interface, &group, NULL, GROUP_EXCLUDE, version);
}

static void test_election_and_recovery(void)
{
    imp_interface *interface = setup();
    imp_interface *other = interface_create("test1", 11);
    query(interface, "192.0.2.30", 3, 100, 2, 125, NULL, NULL);
    query(interface, "192.0.2.20", 3, 100, 2, 125, NULL, NULL);
    query(interface, "0.0.0.0", 3, 100, 2, 125, NULL, NULL);
    assert(interface->gq_timer->tm.tv_sec == 102);
    lower(interface);
    /* Original source fails here: valid lower Query remains unhandled. */
    assert(interface->gq_timer->tm.tv_sec == 355);
    assert(other->gq_timer->tm.tv_sec == 102);
    imp_interface_cleanup(other);
    now.tv_sec = 200;
    lower(interface);
    assert(interface->gq_timer->tm.tv_sec == 455);
    now.tv_sec = 355;
    imp_check_timer();
    assert(general_sent == 0);
    now.tv_sec = 455;
    now.tv_usec = 10000;
    imp_check_timer();
    assert(general_sent == 1);
    assert(interface->gq_timer->tm.tv_sec == 580);
    finish();
}

static void test_query_timings(void)
{
    imp_interface *interface = setup();
    query(interface, "192.0.2.10", 3, 129, 3, 129, NULL, NULL);
    assert(interface->gq_timer->tm.tv_sec == 514);
    assert(interface->gq_timer->tm.tv_usec == 800000);
    query(interface, "192.0.2.10", 3, 100, 0, 0, NULL, NULL);
    assert(interface->gq_timer->tm.tv_sec == 355);
    query(interface, "192.0.2.10", 2, 200, 0, 0, NULL, NULL);
    assert(interface->gq_timer->tm.tv_sec == 360);
    query(interface, "192.0.2.10", 3, 0, 2, 125, NULL, NULL);
    assert(interface->gq_timer->tm.tv_sec == 350);
    assert(group_create(interface, IM_IGMPv3_MLDv2)->timer->tm.tv_sec == 350);
    finish();
}

static void test_specific_queries(void)
{
    imp_interface *interface = setup();
    imp_group *group = group_create(interface, IM_IGMPv3_MLDv2);
    pi_addr a = address("198.51.100.10"), b = address("198.51.100.11");
    imp_source *first = imp_source_create(group, &a, FORWARDING);
    imp_source *second = imp_source_create(group, &b, FORWARDING);
    lower(interface);
    query(interface, "192.0.2.10", 3, 10, 10, 125, "239.192.0.1", NULL);
    assert(group->timer->tm.tv_sec == 360);
    query(interface, "192.0.2.10", 3, 10, 2, 125, "239.192.0.1", "198.51.100.10");
    assert(first->timer->tm.tv_sec == 102);
    assert(second->timer->tm.tv_sec == 360);
    assert(group->timer->tm.tv_sec == 360);
    query(interface, "192.0.2.10", 3, 10, 2, 125, "239.192.0.1", NULL);
    assert(group->timer->tm.tv_sec == 102);
    query(interface, "192.0.2.10", 3, 100, 2, 125, "239.192.0.1", NULL);
    assert(group->timer->tm.tv_sec == 102);
    assert(interface->gq_timer->tm.tv_sec == 355);
    finish();
}

static void test_nonquerier_reports(void)
{
    imp_interface *interface = setup();
    imp_group *group = group_create(interface, IM_IGMPv3_MLDv2);
    pi_addr a = address("198.51.100.10");
    imp_source *source = imp_source_create(group, &a, FORWARDING);
    pa_list *sources = pa_list_add(NULL, &a);
    lower(interface);
    mcast_to_in_handler(interface, &group->group_addr, NULL, IM_IGMPv3_MLDv2);
    assert(group->timer->tm.tv_sec == 360);
    assert(source->timer->tm.tv_sec == 360);
    assert(group->sch_timer == NULL);
    mcast_block_handler(interface, &group->group_addr, sources, IM_IGMPv3_MLDv2);
    assert(source->timer->tm.tv_sec == 360);
    assert(source->times == 0);
    assert(group->sch_timer == NULL);
    mcast_to_ex_hander(interface, &group->group_addr, sources, IM_IGMPv3_MLDv2);
    assert(source->timer->tm.tv_sec == 360);
    assert(group->sch_timer == NULL);
    pa_list_cleanup(&sources);
    finish();
}

static void test_v2_leave_sequence(void)
{
    imp_interface *interface = setup();
    imp_group *group = group_create(interface, IM_IGMPv2_MLDv1);
    struct igmphdr leave;
    memset(&leave, 0, sizeof(leave));
    leave.type = IGMP_HOST_LEAVE_MESSAGE;
    leave.group = group->group_addr.v4.sin_addr.s_addr;
    lower(interface);
    imp_input_report_v1v2(interface, &leave);
    assert(group->timer->tm.tv_sec == 360);
    assert(group->sch_timer == NULL);
    finish();

    interface = setup();
    group = group_create(interface, IM_IGMPv2_MLDv1);
    imp_input_report_v1v2(interface, &leave);
    assert(group->sch_timer != NULL);
    group_source_specific_timer_handler(group);
    assert(specific_sent == 1);
    lower(interface);
    group_source_specific_timer_handler(group);
    assert(specific_sent == 2);
    assert(group->sch_timer == NULL);
    finish();
}

static void test_v2_specific_election(void)
{
    imp_interface *interface = setup();
    mproxy.igmp_version = IM_IGMPv2_MLDv1;
    query(interface, "192.0.2.10", 2, 10, 0, 0, "239.192.0.1", NULL);
    /* The 1-second Last Member response is not the General Query QRI. */
    assert(interface->gq_timer->tm.tv_sec == 355);
    finish();
}

static void test_include_block_sources(void)
{
    imp_interface *interface = setup();
    pi_addr group_address = address("239.192.0.1");
    pi_addr a = address("198.51.100.10"), b = address("198.51.100.11");
    imp_group *group = imp_group_create(interface, &group_address, NULL,
                                       GROUP_INCLUDE, IM_IGMPv3_MLDv2);
    imp_source *first = imp_source_create(group, &a, FORWARDING);
    imp_source *second = imp_source_create(group, &b, FORWARDING);
    pa_list *sources = pa_list_add(NULL, &a);
    sources = pa_list_add(sources, &b);
    mcast_block_handler(interface, &group_address, sources, IM_IGMPv3_MLDv2);
    assert(first->timer->tm.tv_sec == 102);
    assert(second->timer->tm.tv_sec == 102);
    assert(first->times == 2 && second->times == 2);
    assert(group->sch_timer != NULL);
    pa_list_cleanup(&sources);
    finish();
}

int main(void)
{
    test_election_and_recovery();
    test_query_timings();
    test_specific_queries();
    test_nonquerier_reports();
    test_v2_leave_sequence();
    test_v2_specific_election();
    test_include_block_sources();
    puts("PASS: real IGMP receive/election/recovery/membership timer tests");
    return 0;
}
